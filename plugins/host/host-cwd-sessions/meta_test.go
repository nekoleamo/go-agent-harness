// F 组 F0/F2/F3 会话元数据单测:双写兼容 / 迁移 / 置顶与上限 / 概述缓存 / 0600 原子。
package hostcwdsessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// newMetaSvc 构造带会话文件的 Service(t.TempDir 数据根)。
func newMetaSvc(t *testing.T) (*Service, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	s := &Service{key: "proj"}
	root := SessionsRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proj.jsonl", `{"Kind":"user/message","Payload":{"Content":"主会话首条"},"Seq":1}`+"\n")
	write("proj-20260101-1000.jsonl", `{"Kind":"user/message","Payload":{"Content":"A"},"Seq":1}`+"\n"+
		`{"Kind":"assistant/message","Payload":{"Content":"B"},"Seq":2}`+"\n")
	write("proj-20260102-1000.jsonl", `{"Kind":"user/message","Payload":{"Content":"C"},"Seq":1}`+"\n")
	return s, home
}

func TestMetaRenameDoubleWrite(t *testing.T) {
	s, home := newMetaSvc(t)
	if err := s.renameMeta("proj.jsonl", "总纲"); err != nil {
		t.Fatal(err)
	}
	// 双写:meta.json(权威)+ names.json(兼容)
	meta := loadMeta(filepath.Join(home, "sessions", "meta.json"))
	if meta["proj.jsonl"].Name != "总纲" {
		t.Fatalf("meta.json 未写入: %+v", meta)
	}
	names := loadNames(filepath.Join(home, "sessions", "names.json"))
	if names["proj.jsonl"] != "总纲" {
		t.Fatalf("names.json 未同步写入: %+v", names)
	}
	// 权限 0600
	fi, err := os.Stat(filepath.Join(home, "sessions", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("meta.json 权限应为 0600,得 %v", fi.Mode().Perm())
	}
	// SessionName 读 meta
	s.path = filepath.Join(home, "sessions", "proj.jsonl")
	if got := s.SessionName(); got != "总纲" {
		t.Fatalf("SessionName 应读 meta,得 %q", got)
	}
	// 清除名字(空)→ 两处都清
	if err := s.SetName("", ""); err != nil {
		t.Fatal(err)
	}
	if loadMeta(filepath.Join(home, "sessions", "meta.json"))["proj.jsonl"].Name != "" {
		t.Fatal("清除后 meta 不应留名字")
	}
	if loadNames(filepath.Join(home, "sessions", "names.json"))["proj.jsonl"] != "" {
		t.Fatal("清除后 names 不应留名字")
	}
}

// 迁移三态:仅 names.json / 仅 meta.json / 两者冲突(meta 胜)。
func TestMetaMigration(t *testing.T) {
	s, home := newMetaSvc(t)
	namesPath := filepath.Join(home, "sessions", "names.json")
	if err := saveNames(namesPath, map[string]string{"proj-20260101-1000.jsonl": "旧名"}); err != nil {
		t.Fatal(err)
	}
	// 仅 names.json → 名字可见
	s.path = filepath.Join(home, "sessions", "proj.jsonl")
	if got := s.SessionName(); got != "" {
		t.Fatalf("当前会话未命名,得 %q", got)
	}
	found := ""
	for _, si := range s.Sessions() {
		if si.ID == "20260101-1000" {
			found = si.Name
		}
	}
	if found != "旧名" {
		t.Fatalf("应回退 names.json 取名,得 %q", found)
	}
	// 两者冲突:meta 胜
	if err := saveMeta(filepath.Join(home, "sessions", "meta.json"),
		map[string]sessionMeta{"proj-20260101-1000.jsonl": {Name: "新名"}}); err != nil {
		t.Fatal(err)
	}
	for _, si := range s.Sessions() {
		if si.ID == "20260101-1000" && si.Name != "新名" {
			t.Fatalf("冲突时 meta 应优先,得 %q", si.Name)
		}
	}
}

func TestMetaPinned(t *testing.T) {
	s, home := newMetaSvc(t)
	if err := s.SetPinned("20260101-1000", true); err != nil {
		t.Fatal(err)
	}
	list := s.Sessions()
	if len(list) == 0 || !list[0].Pinned || list[0].ID != "20260101-1000" {
		t.Fatalf("置顶项应排首位: %+v", list)
	}
	if list[0].PinnedAt == 0 {
		t.Fatal("应记置顶时间")
	}
	// 幂等
	if err := s.SetPinned("20260101-1000", true); err != nil {
		t.Fatal(err)
	}
	// 取消置顶 → 回 mtime 位置(主会话仍居首)
	if err := s.SetPinned("20260101-1000", false); err != nil {
		t.Fatal(err)
	}
	list = s.Sessions()
	if list[0].Pinned {
		t.Fatalf("取消置顶未生效: %+v", list[0])
	}
	// 上限 8:造 9 个会话文件
	root := SessionsRoot()
	for i := 0; i < 9; i++ {
		name := filepath.Join(root, "proj-2026020"+string(rune('0'+i))+"-1000.jsonl")
		if err := os.WriteFile(name, []byte(`{"Kind":"user/message","Payload":{"Content":"x"},"Seq":1}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 8; i++ {
		id := "2026020" + string(rune('0'+i)) + "-1000"
		if err := s.SetPinned(id, true); err != nil {
			t.Fatalf("第 %d 个置顶应成功: %v", i+1, err)
		}
	}
	if err := s.SetPinned("20260208-1000", true); err == nil {
		t.Fatal("超过上限应显式报错")
	}
	// 不存在的会话 → 报错
	if err := s.SetPinned("nope", true); err == nil {
		t.Fatal("不存在会话应报错")
	}
	// 删除会话 → 元数据无残留
	if err := s.Delete("20260101-1000"); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadMeta(filepath.Join(home, "sessions", "meta.json"))["proj-20260101-1000.jsonl"]; ok {
		t.Fatal("删除会话后 meta 条目应一并移除")
	}
}

func TestMetaSummaryCache(t *testing.T) {
	s, home := newMetaSvc(t)
	sum := sdk.SessionSummary{Text: "修了 provider 鉴权头", Topics: []string{"provider", "鉴权"}, CoveredFrames: 2, Model: "deepseek-chat", TS: 123}
	if err := s.SetSummary("20260101-1000", sum); err != nil {
		t.Fatal(err)
	}
	var got sdk.SessionInfo
	for _, si := range s.Sessions() {
		if si.ID == "20260101-1000" {
			got = si
		}
	}
	if got.Summary != sum.Text || len(got.SummaryTopics) != 2 {
		t.Fatalf("概述未回读: %+v", got)
	}
	if got.SummaryState != "ready" {
		t.Fatalf("覆盖帧数 = 当前帧数应 ready,得 %q", got.SummaryState)
	}
	// 会话又更新 → stale
	f, err := os.OpenFile(filepath.Join(home, "sessions", "proj-20260101-1000.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"Kind":"assistant/message","Payload":{"Content":"D"},"Seq":3}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, si := range s.Sessions() {
		if si.ID == "20260101-1000" && si.SummaryState != "stale" {
			t.Fatalf("新帧后应 stale,得 %q", si.SummaryState)
		}
	}
	// 未生成 → missing
	for _, si := range s.Sessions() {
		if si.ID == "20260102-1000" && si.SummaryState != "missing" {
			t.Fatalf("未生成应 missing,得 %q", si.SummaryState)
		}
	}
	// 不存在的会话写概述 → 报错
	if err := s.SetSummary("nope", sum); err == nil {
		t.Fatal("不存在会话写概述应报错")
	}
}

// 排序:置顶区(pinned_at 倒序)→ 主会话 → 其余 mtime 倒序。
func TestSortSessionsByPinned(t *testing.T) {
	in := []sdk.SessionInfo{
		{ID: "a", MTime: 100},
		{ID: "b", MTime: 300},
		{ID: "", MTime: 50},
		{ID: "c", MTime: 200, Pinned: true, PinnedAt: 10},
		{ID: "d", MTime: 10, Pinned: true, PinnedAt: 20},
	}
	sortSessionsByPinned(in)
	want := []string{"d", "c", "", "b", "a"}
	for i, w := range want {
		if in[i].ID != w {
			t.Fatalf("位置 %d 应为 %q,得 %q(全序 %+v)", i, w, in[i].ID, in)
		}
	}
}
