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
	// 切换到真实目录:key 派生 + 新建空会话 + 记录 dir 为真实目录 + 广播事件
	id, err := svc.SwitchDir(proj)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || svc.Current() != sdk.ProjectKey(proj) || svc.CurrentSession() != id {
		t.Fatalf("切换后 key/会话不符: key=%s current=%s", svc.Current(), svc.CurrentSession())
	}
	if cwd, _ := os.Getwd(); cwd != proj {
		// macOS /var 为 /private/var 符号链接:归一化后比较
		rp, _ := filepath.EvalSymlinks(proj)
		if cwd != rp {
			t.Fatalf("进程 cwd 应切到 %s,实际 %s", proj, cwd)
		}
	}
	if len(emitted) != 1 || emitted[0] != proj {
		t.Fatalf("切换应广播 cwd/workspace-switched(dir=%s),got %v", proj, emitted)
	}
	// 同项目再次切换(touch):不发事件(无实际目录变化)
	_ = emitted
	if _, err := svc.SwitchDir(proj); err != nil {
		t.Fatal(err)
	}
	if len(emitted) != 1 {
		t.Fatalf("同项目切换不应重复广播,got %v", emitted)
	}
	t.Chdir(tmp) // 恢复(cwd 已被本进程改掉)
	recs := svc.RecentProjects()
	if len(recs) != 1 || recs[0].Dir != proj {
		t.Fatalf("记录 dir 应为真实目录: %+v", recs)
	}
	// 目录不可用 → 显式失败(不静默)
	if _, err := svc.SwitchDir(filepath.Join(tmp, "no-such-dir")); err == nil {
		t.Fatalf("不存在目录应显式失败")
	}
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
