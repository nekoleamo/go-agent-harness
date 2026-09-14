// 项目 key 派生与会话多会话(列表/切换/新建)测试。
package hostcwdsessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestProjectKey(t *testing.T) {
	cases := map[string]string{
		"/Users/nekoleamo/Documents/Working/go-agent-harness": "Users-nekoleamo-Documents-Working-go-agent-harness",
		`C:\Users\dev\proj`: "C--Users-dev-proj", // 对齐 dsc 风格:C:\ → C--...

		"/":         "default",
		"/tmp/a/b/": "tmp-a-b",
	}
	for in, want := range cases {
		if got := ProjectKey(in); got != want {
			t.Errorf("ProjectKey(%s) = %q,want %q", in, got, want)
		}
	}
}

func TestServiceListEmpty(t *testing.T) {
	tmp := t.TempDir()
	// 无 sessions 目录 → 空列表
	service := &Service{key: "k", path: filepath.Join(tmp, "k.jsonl")}
	if err := os.Setenv("GAH_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	if list := service.List(); list != nil {
		t.Fatalf("无目录应为空,got %v", list)
	}
}

func TestServiceListWithSession(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// 造两个会话文件
	os.WriteFile(filepath.Join(root, "key-a.jsonl"), []byte("line1\n"), 0o644)
	os.WriteFile(filepath.Join(root, "key-b.jsonl"), []byte("line1\n"), 0o644)
	os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("x"), 0o644)
	if err := os.Setenv("GAH_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	service := &Service{key: "key-a", path: filepath.Join(root, "key-a.jsonl")}
	list := service.List()
	if len(list) != 2 || list[0] != "key-a" || list[1] != "key-b" {
		t.Fatalf("列表应含排序后的两个会话,got %v", list)
	}
	if service.Current() != "key-a" || !strings.HasSuffix(service.Path(), "key-a.jsonl") {
		t.Fatalf("Current/Path 不符: %s %s", service.Current(), service.Path())
	}
}

// fakeSessions ctx.sessions 最小 fake(记录 Load 目标)。同包测试防 import 环。
type fakeSessions struct {
	path   string
	loaded string
}

func (f *fakeSessions) Append(sdk.SessionEvent) error                 { return nil }
func (f *fakeSessions) DeriveMessages() []sdk.LLMMessage              { return nil }
func (f *fakeSessions) Replay() []sdk.SessionEvent                    { return nil }
func (f *fakeSessions) Flush() error                                  { return nil }
func (f *fakeSessions) SetPath(p string)                              { f.path = p }
func (f *fakeSessions) Load(p string) error                           { f.loaded = p; f.path = p; return nil }
func (f *fakeSessions) SetHistory(int)                                {}
func (f *fakeSessions) RegisterCompressor(int, sdk.SessionCompressor) {}

func TestSessionPath(t *testing.T) {
	if got := SessionPath("/r", "k", ""); got != filepath.Join("/r", "k.jsonl") {
		t.Fatalf("主会话路径: %q", got)
	}
	if got := SessionPath("/r", "k", "20240101-1200"); got != filepath.Join("/r", "k-20240101-1200.jsonl") {
		t.Fatalf("切换会话路径: %q", got)
	}
}

func TestSessionsListOrder(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "key.jsonl")
	a := filepath.Join(root, "other.jsonl") // 其他项目会话,应排除
	b := filepath.Join(root, "key-20240101-1200.jsonl")
	c := filepath.Join(root, "key-20240102-1300.jsonl")
	os.WriteFile(main, []byte("e1\ne2\n"), 0o644)
	os.WriteFile(a, []byte("x\n"), 0o644)
	os.WriteFile(b, []byte("e1\n"), 0o644)
	os.WriteFile(c, []byte("e1\ne2\ne3\n"), 0o644)
	// c 修改时间新于 b(倒序预期 c 在前)
	older := time.Now().Add(-2 * time.Hour)
	os.Chtimes(b, older, older)
	if err := os.Setenv("GAH_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	svc := &Service{key: "key", path: main}
	list := svc.Sessions()
	if len(list) != 3 {
		t.Fatalf("应 3 个会话(主+b+c),got %d: %+v", len(list), list)
	}
	// 主会话置顶;切换会话按 mtime 倒序(c> b)
	if list[0].ID != "" || list[1].ID != "20240102-1300" || list[2].ID != "20240101-1200" {
		t.Fatalf("排序不符: %+v", list)
	}
	if list[1].Frames != 3 || list[2].Frames != 1 {
		t.Fatalf("frames 应统计行数: %+v", list)
	}
}

func TestOpenSwitch(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("GAH_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSessions{}
	main := filepath.Join(root, "key.jsonl")
	svc := &Service{key: "key", path: main, sessions: fs}
	if err := svc.Open(""); err != nil {
		t.Fatal(err)
	}
	if svc.CurrentSession() != "" || svc.Path() != main || fs.loaded != main {
		t.Fatalf("主会话切换: current=%q path=%q loaded=%q", svc.CurrentSession(), svc.Path(), fs.loaded)
	}
	id := "20240103-1400"
	if err := svc.Open(id); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "key-"+id+".jsonl")
	if svc.CurrentSession() != id || svc.Path() != want || fs.loaded != want {
		t.Fatalf("切换会话: current=%q path=%q loaded=%q", svc.CurrentSession(), svc.Path(), fs.loaded)
	}
}

func TestNewSession(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Setenv("GAH_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSessions{}
	svc := &Service{key: "key", path: filepath.Join(tmp, "sessions", "key.jsonl"), sessions: fs}
	id, err := svc.New()
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || !strings.HasPrefix(id, time.Now().Format("20060102")) {
		t.Fatalf("新会话 id 应为时间戳格式: %q", id)
	}
	if svc.CurrentSession() != id || !strings.HasSuffix(svc.Path(), "key-"+id+".jsonl") {
		t.Fatalf("New 应切换: current=%q path=%q", svc.CurrentSession(), svc.Path())
	}
	if !strings.HasSuffix(fs.loaded, "key-"+id+".jsonl") {
		t.Fatalf("New 应 Load 新路径: %q", fs.loaded)
	}
}

// TestSwitchProject /workspace 语义:key 重绑 + 自动新建空会话;同项目 no-op。
func TestSwitchProject(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Setenv("GAH_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSessions{}
	svc := &Service{key: "old-proj", sessions: fs}
	if _, err := svc.New(); err != nil { // 先有当前会话
		t.Fatal(err)
	}
	id, err := svc.SwitchProject("new-proj")
	if err != nil {
		t.Fatal(err)
	}
	if svc.key != "new-proj" {
		t.Fatalf("key 应重绑: %q", svc.key)
	}
	if svc.current != id {
		t.Fatalf("应切到新会话: id=%s current=%s", id, svc.current)
	}
	if !strings.HasSuffix(svc.Path(), "new-proj-"+id+".jsonl") {
		t.Fatalf("落盘应切到新项目: %q", svc.Path())
	}
	if !strings.HasSuffix(fs.loaded, "new-proj-"+id+".jsonl") {
		t.Fatalf("应 Load 新项目文件: %q", fs.loaded)
	}
	// 同项目切换 no-op
	id2, err := svc.SwitchProject("new-proj")
	if err != nil || id2 != id || svc.current != id {
		t.Fatalf("同项目应 no-op: id2=%s current=%s", id2, svc.current)
	}
	// 空 key → default
	if _, err := svc.SwitchProject(""); err != nil || svc.key != "default" {
		t.Fatalf("空 key 应落 default: %q err=%v", svc.key, err)
	}
}

// TestRecentProjects 最近使用工作区:记录 upsert 刷新时间、按最近使用倒序、坏文件容忍。
func TestRecentProjects(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Setenv("GAH_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	svc := &Service{key: "p1"}
	svc.recordProject("p1", "/dir/one")
	svc.recordProject("p2", "/dir/two")
	svc.recordProject("p3", "/dir/three")
	// p1 刷新(变最近)
	svc.recordProject("p1", "/dir/one")
	recs := svc.RecentProjects()
	if len(recs) != 3 {
		t.Fatalf("应 3 条: %d", len(recs))
	}
	if recs[0].Key != "p1" || recs[1].Key != "p3" && recs[1].Key != "p2" {
		// p2/p3 同秒顺序不定;只验证 p1 最前与倒序不变量
		t.Fatalf("p1 应最前(时间刷新): %+v", recs)
	}
	for i := 1; i < len(recs); i++ {
		if recs[i].TS > recs[i-1].TS {
			t.Fatalf("应按时间倒序: %+v", recs)
		}
	}
	if recs[0].Dir != "/dir/one" {
		t.Fatalf("Dir 应保留真实路径: %+v", recs[0])
	}
	// 坏文件容忍
	os.WriteFile(filepath.Join(tmp, "sessions", "workspaces.json"), []byte("{broken"), 0o600)
	if got := svc.RecentProjects(); len(got) != 0 {
		t.Fatalf("坏 json 应为空: %v", got)
	}
	// SwitchProject 记录新 key 且同 key 不建新会话(刷新时间)
	svc2 := &Service{key: "p1", sessions: nil}
	if _, err := svc2.New(); err != nil {
		t.Fatal(err)
	}
	id := svc2.current
	if _, err := svc2.SwitchProject("p9"); err != nil {
		t.Fatal(err)
	}
	if svc2.key != "p9" {
		t.Fatalf("应切到 p9: %q", svc2.key)
	}
	// 同 key no-op 返回当前 id
	id2, err := svc2.SwitchProject("p9")
	if err != nil || id2 != svc2.current {
		t.Fatalf("同 key no-op: %v id2=%s", err, id2)
	}
	recs2 := svc2.RecentProjects()
	found := false
	for _, r := range recs2 {
		if r.Key == "p9" {
			found = true
		}
	}
	if !found {
		t.Fatalf("SwitchProject 应记录 p9: %+v", recs2)
	}
	_ = id
}

// --- 会话显示名(/name)测试 ---

// withGahHome 临时 GAH_HOME(测试隔离;恢复原值)。
func withGahHome(t *testing.T, dir string) {
	t.Helper()
	prev := os.Getenv("GAH_HOME")
	os.Setenv("GAH_HOME", dir)
	t.Cleanup(func() { os.Setenv("GAH_HOME", prev) })
}

func TestSessionRenamePersistAndIndependent(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	withGahHome(t, tmp)
	// 主会话 + 一个切换会话文件
	os.WriteFile(filepath.Join(root, "k.jsonl"), []byte("e1\n"), 0o644)
	swPath := filepath.Join(root, "k-20261002-100000.jsonl")
	os.WriteFile(swPath, []byte("e1\n"), 0o644)
	// 分别命名(按会话独立)
	main := &Service{key: "k", path: filepath.Join(root, "k.jsonl")}
	if err := main.Rename("总纲"); err != nil {
		t.Fatal(err)
	}
	sw := &Service{key: "k", path: swPath}
	if err := sw.Rename("重构排期"); err != nil {
		t.Fatal(err)
	}
	if got := main.SessionName(); got != "总纲" {
		t.Fatalf("主会话名,got %q", got)
	}
	// 重启等价:新实例(仅 key)读 names.json 恢复主会话名;Sessions 列表两者独立带名
	svc2 := &Service{key: "k", path: filepath.Join(root, "k.jsonl")}
	if got := svc2.SessionName(); got != "总纲" {
		t.Fatalf("重启后主会话名应保留,got %q", got)
	}
	names := map[string]string{}
	for _, si := range svc2.Sessions() {
		names[si.ID] = si.Name
	}
	if names[""] != "总纲" {
		t.Fatalf("Sessions 主会话 Name 应带,got %q", names[""])
	}
	if names["20261002-100000"] != "重构排期" {
		t.Fatalf("Sessions 切换会话 Name 应独立,got %q", names["20261002-100000"])
	}
}

func TestSessionRenameClear(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	withGahHome(t, tmp)
	p := filepath.Join(root, "k-1.jsonl")
	os.WriteFile(p, []byte("e1\n"), 0o644)
	svc := &Service{key: "k", path: p}
	if err := svc.Rename("临时名"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Rename(""); err != nil { // 清除
		t.Fatal(err)
	}
	if got := svc.SessionName(); got != "" {
		t.Fatalf("清除后应无名,got %q", got)
	}
	svc2 := &Service{key: "k", path: p}
	if got := svc2.SessionName(); got != "" {
		t.Fatalf("清除应持久,got %q", got)
	}
}

func TestSessionNamesCorruptTolerated(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	withGahHome(t, tmp)
	p := filepath.Join(root, "k-1.jsonl")
	os.WriteFile(p, []byte("e1\n"), 0o644)
	// 坏 names.json:读=未命名不报错;Rename 覆写修复后可继续
	os.WriteFile(filepath.Join(root, "names.json"), []byte("{{{not-json"), 0o644)
	svc := &Service{key: "k", path: p}
	if got := svc.SessionName(); got != "" {
		t.Fatalf("坏索引应视为未命名,got %q", got)
	}
	if err := svc.Rename("ok"); err != nil {
		t.Fatalf("坏索引下 Rename 应可覆写修复: %v", err)
	}
	if got := svc.SessionName(); got != "ok" {
		t.Fatalf("修复后应读到名,got %q", got)
	}
	// 新实例读修复后文件正常
	svc2 := &Service{key: "k", path: p}
	if got := svc2.SessionName(); got != "ok" {
		t.Fatalf("修复后持久,got %q", got)
	}
}

func TestPreview(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("GAH_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "key.jsonl")
	user := `{"Kind":"user/message","Payload":{"Content":"帮我优化 web 端页面,要求界面清新简洁大方"},"Seq":1}`
	asst := `{"Kind":"assistant/chunk","Payload":{"Delta":"好的"},"Seq":2}`
	os.WriteFile(main, []byte(user+"\n"+asst+"\n"), 0o644)
	svc := &Service{key: "key", path: main}
	list := svc.Sessions()
	if len(list) != 1 || !strings.Contains(list[0].Preview, "帮我优化 web 端页面") {
		t.Fatalf("Preview 应取首条用户消息: %+v", list)
	}
	// 长文本截断 + 省略号
	long := strings.Repeat("很长", 60)
	os.WriteFile(main, []byte(`{"Kind":"user/message","Payload":{"Content":"`+long+`"},"Seq":1}`+"\n"), 0o644)
	if p := previewOf(main, 48); !strings.HasSuffix(p, "…") || len([]rune(p)) > 49 {
		t.Fatalf("超长应截断加省略号: %d %q", len([]rune(p)), p)
	}
}

func TestDelete(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("GAH_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "key.jsonl")
	idf := filepath.Join(root, "key-20240101-1200.jsonl")
	os.WriteFile(main, []byte("e1\n"), 0o644)
	os.WriteFile(idf, []byte("e1\n"), 0o644)
	fs := &fakeSessions{}
	svc := &Service{key: "key", path: idf, sessions: fs, current: "20240101-1200"}
	// 名称索引
	svc.Rename("历史会话")
	// 删除非当前会话不存在 = 报错
	if err := svc.Delete("nope"); err == nil {
		t.Fatalf("删除不存在的会话应报错")
	}
	// 删除当前打开会话 → 自动新建空会话承接(干净新起点)
	if err := svc.Delete("20240101-1200"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(idf); !os.IsNotExist(err) {
		t.Fatalf("会话文件应被删除")
	}
	if svc.CurrentSession() == "" || svc.CurrentSession() == "20240101-1200" {
		t.Fatalf("删除当前会话后应新开空会话,got %q", svc.CurrentSession())
	}
	// 名称索引同步清理
	if got := svc.SessionName(); got != "" {
		t.Fatalf("删除后名称索引应清理,got %q", got)
	}
}

func TestUnrecordProject(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("GAH_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	svc := &Service{key: "k"}
	recs := []sdk.ProjectInfo{
		{Key: "a", Dir: "/a", TS: 3},
		{Key: "b", Dir: "/b", TS: 2},
	}
	if err := saveWorkspaces(workspacesPath(), recs); err != nil {
		t.Fatal(err)
	}
	if err := svc.UnrecordProject("a"); err != nil {
		t.Fatal(err)
	}
	got := svc.RecentProjects()
	if len(got) != 1 || got[0].Key != "b" {
		t.Fatalf("应仅剩 b: %+v", got)
	}
	// 不存在 key 幂等
	if err := svc.UnrecordProject("zzz"); err != nil {
		t.Fatalf("幂等删除应成功: %v", err)
	}
}

func TestSwitchDir(t *testing.T) {
	orig, _ := os.Getwd()
	tmp := t.TempDir()
	if err := os.Setenv("GAH_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(tmp, "proj-a")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(tmp, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mainP := filepath.Join(root, "k.jsonl")
	os.WriteFile(mainP, []byte("e1\n"), 0o644)
	fs := &fakeSessions{}
	emitted := []string{}
	svc := &Service{key: "k", path: mainP, sessions: fs, emitWS: func(d string) { emitted = append(emitted, d) }}
	if err := svc.Open(""); err != nil {
		t.Fatal(err)
	}
	// SwitchDir 内部归一化符号链接与 Windows 8.3 短名,key/广播/记录统一用归一化路径:
	// 否则同一目录在不同形态下(macOS /var 与 /private/var、Windows 短名与长名)
	// 会派生两个 key(工作区历史重复),且与 SwitchProject(currentDir 派生)不一致。
	rp, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatal(err)
	}
	// 切换到真实目录:key 派生 + 新建空会话 + 记录 dir 为真实目录 + 广播事件
	id, err := svc.SwitchDir(proj)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || svc.Current() != sdk.ProjectKey(rp) || svc.CurrentSession() != id {
		t.Fatalf("切换后 key/会话不符: key=%s current=%s", svc.Current(), svc.CurrentSession())
	}
	if cwd, _ := os.Getwd(); cwd != rp {
		t.Fatalf("进程 cwd 应切到 %s,实际 %s", rp, cwd)
	}
	if len(emitted) != 1 || emitted[0] != rp {
		t.Fatalf("切换应广播 cwd/workspace-switched(dir=%s),got %v", rp, emitted)
	}
	// 同项目再次切换(touch):不发事件(无实际目录变化)
	_ = emitted
	if _, err := svc.SwitchDir(proj); err != nil {
		t.Fatal(err)
	}
	if len(emitted) != 1 {
		t.Fatalf("同项目切换不应重复广播,got %v", emitted)
	}
	t.Chdir(orig) // 中途恢复原 cwd(下方"同项目 touch"还会再切回 proj-a)
	recs := svc.RecentProjects()
	if len(recs) != 1 || recs[0].Dir != rp {
		t.Fatalf("记录 dir 应为归一化真实路径: %+v", recs)
	}
	// 目录不可用 → 显式失败(不静默)
	if _, err := svc.SwitchDir(filepath.Join(tmp, "no-such-dir")); err == nil {
		t.Fatalf("不存在目录应显式失败")
	}

	// 结束前必须把 cwd 还原:SwitchDir 头一行就无条件 os.Chdir(dir),上面那次
	// 「同项目 touch」又把 cwd 切回了 proj-a —— Windows 不允许删除当前工作目录,
	// 不复原会让 t.TempDir() 清理时报 unlinkat Access is denied(POSIX 无此限制)。
	// 放在最后注册,cleanup 的 LIFO 顺序保证它先于 TempDir 的 RemoveAll 执行。
	t.Chdir(orig)
}

// TestSessionSwitchEmitted Open/New 广播 cwd/session-switched(B3 命令下沉 UI 刷新驱动)。
func TestSessionSwitchEmitted(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GAH_HOME", root) // SessionsRoot 隔离(New 的文件存在性检查不落真实 ~/.gah)
	var got []string
	svc := &Service{key: "k", sessions: nil, emitSession: func(id string) { got = append(got, id) }}
	if err := svc.Open("s1"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "s1" {
		t.Fatalf("Open 应广播 s1,got %v", got)
	}
	if _, err := svc.New(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] == "" || got[1] == "s1" {
		t.Fatalf("New 应广播新 id,got %v", got)
	}
}

// TestOpenRejectsInvalidSessionID Open 必须校验会话 id:未过滤的 id 经 filepath.Join
// 会 Clean 掉 ".." 段,让会话落到 $GAH_HOME 之外(Delete 早已校验,Open 曾漏同一防线)。
func TestOpenRejectsInvalidSessionID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	s := &Service{key: "k"}
	for _, bad := range []string{"../../poc", "a/b", "..", "a.b", `x\y`} {
		if err := s.Open(bad); err == nil {
			t.Fatalf("非法会话 id 必须拒绝: %q", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(home), "poc.jsonl")); err == nil {
		t.Fatal("不得在数据根之外创建会话文件")
	}
	// 合法 id(时间戳形态)仍可用
	if err := s.Open("20260101-000000"); err != nil {
		t.Fatalf("合法 id 应可用: %v", err)
	}
	if s.CurrentSession() != "20260101-000000" {
		t.Fatalf("切换后 current 应更新: %s", s.CurrentSession())
	}
	// 主会话(id 空)不受影响
	if err := s.Open(""); err != nil {
		t.Fatalf("主会话应可用: %v", err)
	}
}

// TestCorruptIndexQuarantined 解析失败的覆写式索引须改名留存(.corrupt-*),
// 不得被后续"空表覆写"静默抹掉。
func TestCorruptIndexQuarantined(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(SessionsRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(SessionsRoot(), "workspaces.json")
	if err := os.WriteFile(bad, []byte("{半截:{"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Service{key: "k"}
	if got := s.RecentProjects(); len(got) != 0 {
		t.Fatalf("坏索引应容忍为空: %+v", got)
	}
	// 原文件已被隔离留存(可人工抢救),不会在读后覆写中丢失
	if _, err := os.Stat(bad); err == nil {
		t.Fatal("损坏索引应被改名留存,而非原地保留待覆写")
	}
	entries, err := os.ReadDir(SessionsRoot())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "workspaces.json.corrupt-") {
			found = true
		}
	}
	if !found {
		t.Fatal("未找到 .corrupt-* 隔离文件")
	}
}

// TestIndexAtomicOverwrite 覆写式索引落盘后必须可完整读回(原子写:tmp+rename 不留半截)。
func TestIndexAtomicOverwrite(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	s := &Service{key: "k"}
	s.recordProject("k", "/tmp/proj")
	recs := s.RecentProjects()
	if len(recs) != 1 || recs[0].Key != "k" || recs[0].Dir != "/tmp/proj" {
		t.Fatalf("记录往返不符: %+v", recs)
	}
	m := map[string]string{"k.jsonl": "名字"}
	if err := saveNames(sessionNamesPath(), m); err != nil {
		t.Fatal(err)
	}
	if got := loadNames(sessionNamesPath()); got["k.jsonl"] != "名字" {
		t.Fatalf("显示名索引往返不符: %+v", got)
	}
	// 目录里不得残留临时文件
	entries, err := os.ReadDir(SessionsRoot())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".idx-") {
			t.Fatalf("残留临时文件: %s", e.Name())
		}
	}
}
