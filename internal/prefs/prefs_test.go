// prefs 共享偏好持久化测试:roundtrip、legacy 迁移、无 GAH_HOME 跳过。
package prefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrefsRoundtrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)

	h := 12
	Save(Prefs{Thinking: "medium", Sandbox: "full-access", Approval: "smart", History: &h})
	got := Load()
	if got.Thinking != "medium" || got.Sandbox != "full-access" || got.Approval != "smart" || got.History == nil || *got.History != 12 {
		t.Fatalf("roundtrip 不符: %+v", got)
	}
	// 便捷更新
	SetThinking("high")
	SetSandbox("read-only")
	SetApproval("strict")
	SetHistory(-1)
	got = Load()
	if got.Thinking != "high" || got.Sandbox != "read-only" || got.Approval != "strict" || got.History == nil || *got.History != -1 {
		t.Fatalf("便捷更新不符: %+v", got)
	}
	// 文件确实写在新名 gah-state.json
	if _, err := os.Stat(filepath.Join(home, "config", "gah-state.json")); err != nil {
		t.Fatalf("应写 gah-state.json: %v", err)
	}
}

func TestPrefsLegacyMigrate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	dir := filepath.Join(home, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 旧文件 web-state.json(无新文件)→ Load 回退读取
	if err := os.WriteFile(filepath.Join(dir, "web-state.json"), []byte(`{"thinking":"low","sandbox":"workspace-write"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.Thinking != "low" || got.Sandbox != "workspace-write" {
		t.Fatalf("legacy 回退读不符: %+v", got)
	}
	// 无 GAH_HOME → Load 零值、Save no-op(不 panic)
	t.Setenv("GAH_HOME", "")
	if l := Load(); l.Thinking != "" || l.History != nil {
		t.Fatalf("无 GAH_HOME Load 应零值: %+v", l)
	}
	Save(Prefs{Thinking: "high"}) // no-op
}

// 首启引导关闭记录(G-E4-R):幂等、空 id 忽略、与其它偏好共存。
func TestPrefsGuideDismissed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if GuideDismissed("desktop-im") || GuideDismissed("") {
		t.Fatal("初始不应为已关闭")
	}
	SetGuideDismissed("")
	SetGuideDismissed("desktop-im")
	SetGuideDismissed("desktop-im") // 幂等
	if !GuideDismissed("desktop-im") {
		t.Fatal("应记为已关闭")
	}
	SetGuideDismissed("other")
	p := Load()
	if len(p.DismissedGuides) != 2 {
		t.Fatalf("应保留 2 条且不重复: %+v", p.DismissedGuides)
	}
	// 其它偏好不受影响(读-改-写共享同一文件)
	SetThinking("high")
	SetGuideDismissed("third")
	got := Load()
	if got.Thinking != "high" || len(got.DismissedGuides) != 3 || !GuideDismissed("desktop-im") {
		t.Fatalf("共存异常: %+v", got)
	}
}
