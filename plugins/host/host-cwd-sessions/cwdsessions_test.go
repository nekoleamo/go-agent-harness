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
