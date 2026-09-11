// prefs 共享偏好持久化测试:roundtrip、legacy 迁移、无 GAH_HOME 跳过。
package prefs

import (
	"os"
	"path/filepath"
	"sync"
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

// TestUpdateKeepsConcurrentFields 并发改不同字段必须都保留(Update 锁内读-改-写;
// 旧实现 Load+Save 整对象覆写会让后写者抹掉先写者的字段)。
func TestUpdateKeepsConcurrentFields(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(3)
		go func(n int) { defer wg.Done(); Update(func(p *Prefs) { p.Thinking = "high" }) }(i)
		go func(n int) { defer wg.Done(); Update(func(p *Prefs) { p.Sandbox = "workspace-write" }) }(i)
		go func(n int) { defer wg.Done(); Update(func(p *Prefs) { p.Approval = "smart" }) }(i)
	}
	wg.Wait()
	p := Load()
	if p.Thinking != "high" || p.Sandbox != "workspace-write" || p.Approval != "smart" {
		t.Fatalf("并发改不同字段出现互相覆盖: %+v", p)
	}
}
