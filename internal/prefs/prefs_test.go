// prefs 共享偏好持久化测试:roundtrip、legacy 迁移、无 GAH_HOME 跳过。
package prefs

import (
	"os"
	"path/filepath"
	"strings"
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

// TestSandboxSyncPrefsTriState 联动开关是**三态**:nil = 未设置(用插件 config 默认)、
// true/false = 用户显式选择。零值不能与"显式 false"混淆,否则「关掉联动」会被当成没设置。
func TestSandboxSyncPrefsTriState(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	if v := Load().SandboxSync; v != nil {
		t.Fatalf("初始应为 nil(未设置),got %v", *v)
	}
	SetSandboxSync(false)
	v := Load().SandboxSync
	if v == nil || *v {
		t.Fatalf("显式 false 应被记住,got %v", v)
	}
	// 与其它字段互不干扰(Update 是读-改-写,不整体覆写)
	SetApproval("strict")
	if v := Load().SandboxSync; v == nil || *v {
		t.Fatal("写别的字段不得抹掉 sandbox_sync")
	}
	SetSandboxSync(true)
	if v := Load().SandboxSync; v == nil || !*v {
		t.Fatalf("显式 true 应被记住,got %v", v)
	}
	// 序列化形状:显式 false 必须落进 JSON(omitempty 不该吃掉 false)
	raw, err := os.ReadFile(filepath.Join(os.Getenv("GAH_HOME"), "config", "gah-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"sandbox_sync":true`) {
		t.Fatalf("应落 JSON 字段: %s", raw)
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
