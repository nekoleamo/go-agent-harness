// mcpconfig 单测:$GAH_HOME 路径派生、文件往返/权限/原子写、env 兼容解析、
// 文件优先合并、规范化(拆分/校验)。全部经 t.Setenv("GAH_HOME", t.TempDir()) 隔离。
package mcpconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
)

// setupHome 隔离数据根(便携纪律:所有写盘必须落 GAH_HOME 下)。
func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	return home
}

func boolPtr(b bool) *bool { return &b }

func TestPathUnderHomeAndLoadMissing(t *testing.T) {
	home := setupHome(t)
	want := filepath.Join(home, "config", "mcp.yaml")
	if got := Path(); got != want {
		t.Fatalf("Path() 应落 GAH_HOME/config/mcp.yaml: %s", got)
	}
	f, err := LoadFile()
	if err != nil {
		t.Fatalf("文件不存在不应报错: %v", err)
	}
	if len(f.Servers) != 0 {
		t.Fatalf("不存在时应为空: %+v", f)
	}
	// 空文件同样视为空配置(不报错)
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f, err = LoadFile(); err != nil || len(f.Servers) != 0 {
		t.Fatalf("空文件应为空配置: %+v %v", f, err)
	}
}

func TestSaveLoadRoundTripAndPerm(t *testing.T) {
	setupHome(t)
	if err := Save([]Server{
		{Name: "deja", Command: "/usr/local/bin/deja"},
		{Name: "code graph", Command: "codegraph", Args: []string{"serve", "--mcp"}, Mode: ModeSearch, Enabled: boolPtr(false)},
	}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if testutil.PosixPerm() && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mcp.yaml 权限应为 0600, got %o", fi.Mode().Perm())
	}
	raw, _ := os.ReadFile(Path())
	if !strings.HasPrefix(string(raw), "# gah MCP server 配置") {
		t.Fatalf("文件头应为说明注释: %s", string(raw)[:40])
	}
	f, err := LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Servers) != 2 {
		t.Fatalf("应读回 2 项: %+v", f.Servers)
	}
	a := f.Servers[0]
	if a.Name != "deja" || a.Command != "/usr/local/bin/deja" || !a.IsEnabled() || a.ModeOrDefault() != ModeDirect {
		t.Fatalf("首项字段往返: %+v", a)
	}
	if a.Source != "" {
		t.Fatalf("source 不得落盘: %+v", a)
	}
	b := f.Servers[1]
	if b.Name != "code_graph" || b.Mode != ModeSearch || b.IsEnabled() {
		t.Fatalf("第二项字段往返: %+v", b)
	}
	if len(b.Args) != 2 || b.Args[0] != "serve" {
		t.Fatalf("args 往返: %+v", b.Args)
	}
}

// TestSaveSkipsEnvSourced 只读来源(env)条目不得被固化进文件。
func TestSaveSkipsEnvSourced(t *testing.T) {
	setupHome(t)
	if err := SaveFile(File{Servers: []Server{
		{Name: "fromenv", Command: "/bin/x", Source: SourceEnv},
		{Name: "fromfile", Command: "/bin/y", Source: SourceFile},
	}}); err != nil {
		t.Fatal(err)
	}
	f, _ := LoadFile()
	if len(f.Servers) != 1 || f.Servers[0].Name != "fromfile" {
		t.Fatalf("env 条目不应落盘: %+v", f.Servers)
	}
}

func TestLoadFileBadYAML(t *testing.T) {
	setupHome(t)
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("servers: [oops\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(); err == nil || !strings.Contains(err.Error(), "mcp.yaml 解析失败") {
		t.Fatalf("坏 yaml 应显式报错: %v", err)
	}
}

func TestParseEnvCompat(t *testing.T) {
	env, notes := ParseEnv("", "alpha=/bin/a -x\n# 注释\n\nbad line\nbeta=/bin/b\n=empty\nfoo=/bin/f")
	if len(notes) != 2 {
		t.Fatalf("应有 2 条提示(无效行 + 空名): %v", notes)
	}
	if len(env) != 3 {
		t.Fatalf("有效条目 3 条: %+v", env)
	}
	if env[0].Name != "alpha" || env[0].Command != "/bin/a" || len(env[0].Args) != 1 || env[0].Source != SourceEnv {
		t.Fatalf("多 server 首条: %+v", env[0])
	}
	if env[0].ModeOrDefault() != ModeDirect {
		t.Fatalf("env 条目模式应为 direct(现状语义): %+v", env[0])
	}
	if env[2].Name != "foo" {
		t.Fatalf("空名行应跳过而非占位: %+v", env)
	}
	// 单 server 兼容:name 为空,工具名不带前缀
	single, _ := ParseEnv("/bin/one --serve", "")
	if len(single) != 1 || single[0].Name != "" || single[0].Command != "/bin/one" || single[0].Args[0] != "--serve" {
		t.Fatalf("单 server 兼容解析: %+v", single)
	}
	// 两者并存 = 并集
	both, _ := ParseEnv("/bin/one", "alpha=/bin/a")
	if len(both) != 2 {
		t.Fatalf("单/多并存取并集: %+v", both)
	}
	// 重复名:保留先出现项并提示
	dup, notes2 := ParseEnv("", "a=/bin/1\na=/bin/2")
	if len(dup) != 1 || dup[0].Command != "/bin/1" {
		t.Fatalf("重复名保留先出现项: %+v", dup)
	}
	if len(notes2) != 1 || !strings.Contains(notes2[0], "重复") {
		t.Fatalf("重复名应有提示: %v", notes2)
	}
}

func TestEffectiveFileWins(t *testing.T) {
	file := []Server{{Name: "deja", Command: "/file/deja", Mode: ModeSearch}, {Name: "only-file", Command: "/bin/of"}}
	env := []Server{{Name: "deja", Command: "/env/deja"}, {Name: "only-env", Command: "/bin/oe"}}
	got := Effective(file, env)
	if len(got) != 3 {
		t.Fatalf("文件优先 + env 独有: %+v", got)
	}
	if got[0].Command != "/file/deja" || got[0].Mode != ModeSearch || got[0].Source != SourceFile {
		t.Fatalf("同名文件条目应胜出且标 file: %+v", got[0])
	}
	if got[1].Name != "only-file" || got[2].Name != "only-env" || got[2].Source != SourceEnv {
		t.Fatalf("顺序应为文件后 env: %+v", got)
	}
	// 文件内重名:保留先出现项
	dup := Effective([]Server{{Name: "a", Command: "/1"}, {Name: "a", Command: "/2"}}, nil)
	if len(dup) != 1 || dup[0].Command != "/1" {
		t.Fatalf("文件内重名应去重保先: %+v", dup)
	}
}

func TestLoadMergesFileAndEnv(t *testing.T) {
	setupHome(t)
	t.Setenv("GAH_MCP_COMMANDS", "alpha=/bin/a\nbeta=/bin/b -v")
	t.Setenv("GAH_MCP_COMMAND", "/bin/single")
	if err := Save([]Server{{Name: "beta", Command: "/file/b", Mode: ModeSearch}, {Name: "gamma", Command: "/file/g"}}); err != nil {
		t.Fatal(err)
	}
	got, notes, err := Load()
	if err != nil || len(notes) != 0 {
		t.Fatalf("Load 应无错误/提示: %v %v", err, notes)
	}
	names := []string{}
	for _, s := range got {
		names = append(names, s.Name+"="+s.Command+"("+s.Source+")")
	}
	// 顺序:文件条目 → env(单 server 先,多 server 次;与历史装配顺序一致)
	want := "beta=/file/b(file),gamma=/file/g(file),=/bin/single(env),alpha=/bin/a(env)"
	if strings.Join(names, ",") != want {
		t.Fatalf("合并视图:\n got %s\nwant %s", strings.Join(names, ","), want)
	}
	if got[0].ModeOrDefault() != ModeSearch {
		t.Fatalf("文件条目模式应保留: %+v", got[0])
	}
}

func TestNormalize(t *testing.T) {
	// sanitize + 缺省启用/模式
	s, err := Normalize(Server{Name: " My Server ", Command: "/bin/x"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "My_Server" || !s.IsEnabled() || s.ModeOrDefault() != ModeDirect {
		t.Fatalf("sanitize/默认值: %+v", s)
	}
	// 命令含空格且 args 为空 → 拆分(引号内空格保留)
	s, err = Normalize(Server{Name: "a", Command: `"/Applications/My App/bin/x" --serve --flag=1`})
	if err != nil {
		t.Fatal(err)
	}
	if s.Command != "/Applications/My App/bin/x" || len(s.Args) != 2 || s.Args[0] != "--serve" {
		t.Fatalf("命令拆分(含引号路径): %+v", s)
	}
	// 显式 args 时不做拆分(命令可含空格?不 —— 只有 args 非空才跳过拆分)
	s, err = Normalize(Server{Name: "a", Command: "/bin/x", Args: []string{"--serve"}})
	if err != nil || s.Command != "/bin/x" || len(s.Args) != 1 {
		t.Fatalf("显式 args: %+v %v", s, err)
	}
	// 显式声明 search
	s, _ = Normalize(Server{Name: "a", Command: "/bin/x", Mode: ModeSearch})
	if s.ModeOrDefault() != ModeSearch {
		t.Fatalf("显式 search 保留: %+v", s)
	}
	// 错误:空名/非法 mode/空命令
	for _, bad := range []Server{
		{Name: "  ", Command: "/bin/x"},
		{Name: "a", Command: "/bin/x", Mode: "seach"},
		{Name: "a", Command: "   ", Args: []string{"--x"}},
	} {
		if _, err := Normalize(bad); err == nil {
			t.Fatalf("应报错: %+v", bad)
		}
	}
	if err := Save([]Server{{Name: "dup", Command: "/bin/1"}, {Name: "dup", Command: "/bin/2"}}); err == nil {
		t.Fatal("Save 应拒绝重名")
	}
}

func TestSaveEmptyWritesEmptyList(t *testing.T) {
	setupHome(t)
	if err := Save(nil); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFile()
	if err != nil || len(f.Servers) != 0 {
		t.Fatalf("空列表应可读回: %+v %v", f, err)
	}
	if strings.Contains(string(mustRead(t, Path())), "name:") {
		t.Fatalf("空列表不应含条目: %s", string(mustRead(t, Path())))
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestLoadFileNormalizesHandWritten 手写配置与 GUI 写入共用同一条语义:
// 整行命令拆分为 command+args(含引号)、名字净化、mode 大小写容错。
func TestLoadFileNormalizesHandWritten(t *testing.T) {
	home := setupHome(t)
	raw := `# 手写
servers:
  - name: My Server
    command: npx -y @modelcontextprotocol/server-filesystem "/tmp/a b"
    mode: SEARCH
  - name: plain
    command: /bin/only
`
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Servers) != 2 {
		t.Fatalf("应读回 2 项: %+v", f.Servers)
	}
	a := f.Servers[0]
	if a.Name != "My_Server" {
		t.Fatalf("名字应净化: %q", a.Name)
	}
	if a.Command != "npx" || len(a.Args) != 3 || a.Args[0] != "-y" || a.Args[1] != "@modelcontextprotocol/server-filesystem" {
		t.Fatalf("整行命令应拆分: %+v", a)
	}
	if a.Args[2] != "/tmp/a b" {
		t.Fatalf("引号内空格应保留: %q", a.Args[2])
	}
	if a.ModeOrDefault() != ModeSearch {
		t.Fatalf("mode 应大小写容错并归一: %q", a.Mode)
	}
	// 无空白的命令不拆
	if b := f.Servers[1]; b.Command != "/bin/only" || len(b.Args) != 0 {
		t.Fatalf("单命令不应拆: %+v", b)
	}
	// 显式 args 与 command 同时存在时以 args 为准(不重复拆)
	if err := Save([]Server{{Name: "both", Command: "/bin/x -y", Args: []string{"--real"}}}); err != nil {
		t.Fatal(err)
	}
	f2, err := LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if got := f2.Servers[0]; got.Command != "/bin/x -y" || len(got.Args) != 1 || got.Args[0] != "--real" {
		t.Fatalf("显式 args 优先: %+v", got)
	}
}
