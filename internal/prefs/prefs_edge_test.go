// prefs 的边缘路径补测(第九十四批 · 覆盖率补强):
// 落盘失败不许留垃圾、坏文件不许复活旧偏好、便捷 setter 的边界。
//
// 为什么这些值得测:偏好文件是 TUI 与 Web **同时**写的可写面;写失败本身按设计静默
// (非关键路径),于是"静默"必须是**干净地**静默 —— 不能留下半截临时文件、也不能
// 悄悄把旧 web-state.json 的内容当成新偏好写回去。
package prefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpdateNilCallback nil 回调是空操作(不是 panic,也不该落盘任何东西)。
func TestUpdateNilCallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	Update(nil)
	if _, err := os.Stat(filepath.Join(home, "config", "gah-state.json")); !os.IsNotExist(err) {
		t.Fatalf("nil 回调不该落盘: %v", err)
	}
	if p := Load(); p.Thinking != "" || p.Role != "" {
		t.Fatalf("nil 回调不该改偏好: %+v", p)
	}
}

// TestSaveLockedParentBlockedByFile 目录位置被一个**普通文件**占着(用户手滑或磁盘恢复):
// MkdirAll 失败 → 静默跳过,且不许 panic。偏好属于"丢了也不致命"的一类,但必须可解释。
func TestSaveLockedParentBlockedByFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	// config 是个文件,不是目录 ⇒ 建目录失败
	if err := os.WriteFile(filepath.Join(home, "config"), []byte("占位"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetThinking("high") // 不 panic
	b, err := os.ReadFile(filepath.Join(home, "config"))
	if err != nil || string(b) != "占位" {
		t.Fatalf("占位文件不该被动: %q %v", b, err)
	}
	if p := Load(); p.Thinking != "" {
		t.Fatalf("写入失败时读回应为零值(不假装写成功): %+v", p)
	}
}

// TestWriteFileAtomicFailureLeavesNoTemp 原子写的失败路径(重命名目标不可用):
// 必须报错 + **不留临时文件**。留垃圾会让下次"目录里一堆 .gah-state-*.tmp"变成真问题。
func TestWriteFileAtomicFailureLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	// 目标路径被一个目录占着:同目录临时文件写得进,rename 一定失败
	target := filepath.Join(dir, "gah-state.json")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte(`{"thinking":"high"}`), 0o600); err == nil {
		t.Fatal("目标被目录占着时 rename 应失败")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".gah-state-") {
			t.Fatalf("失败路径留下了临时文件:%s", e.Name())
		}
	}
	if len(entries) != 1 || entries[0].Name() != "gah-state.json" {
		t.Fatalf("目录内容不符:%v", entries)
	}

	// 父目录不存在:CreateTemp 直接失败(同样不留痕)
	missing := filepath.Join(dir, "nope", "gah-state.json")
	if err := writeFileAtomic(missing, []byte("{}"), 0o600); err == nil {
		t.Fatal("父目录不存在时应失败")
	}
}

// TestWriteFileAtomicSuccess 正常路径:内容逐字落盘、不留临时文件。
func TestWriteFileAtomicSuccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gah-state.json")
	raw := []byte(`{"thinking":"high"}`)
	if err := writeFileAtomic(target, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("内容不符: %q %v", got, err)
	}
	// 覆盖已存在的文件(rename 替换)也要成功
	if err := writeFileAtomic(target, []byte(`{"thinking":"low"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != `{"thinking":"low"}` {
		t.Fatalf("覆盖后内容不符: %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".gah-state-") {
			t.Fatalf("成功路径不该留下临时文件:%s", e.Name())
		}
	}
}

// TestLoadCorruptDoesNotReviveLegacy 新文件坏掉时返回零值,**不回退**旧 web-state.json:
// 回退等于把用户早已弃用的旧偏好(可能是 old-sandbox 档)重新变成生效值。
func TestLoadCorruptDoesNotReviveLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	dir := filepath.Join(home, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gah-state.json"), []byte("{坏 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web-state.json"), []byte(`{"sandbox":"full-access"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := Load(); p.Sandbox != "" || p.Thinking != "" {
		t.Fatalf("坏新文件应回落零值(不复活旧文件): %+v", p)
	}
	// 坏文件可被下一次写入覆盖回可用状态(自愈,不需要用户手删)
	SetThinking("medium")
	if p := Load(); p.Thinking != "medium" {
		t.Fatalf("写入应覆盖坏文件: %+v", p)
	}
}

// TestSetRoleAndStatusline 两个跨功能偏好(角色 ID 与状态栏项集合):
// 空值语义要明确 —— Role 空 = 停用角色;Statusline 空 = 回基线默认。
func TestSetRoleAndStatusline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)

	SetRole("finance")
	if got := Load().Role; got != "finance" {
		t.Fatalf("SetRole 未生效: %q", got)
	}
	SetStatusline([]string{"model", "tokens"})
	p := Load()
	if len(p.Statusline) != 2 || p.Statusline[0] != "model" || p.Statusline[1] != "tokens" {
		t.Fatalf("SetStatusline 未生效: %v", p.Statusline)
	}
	// 与其它字段互不干扰(Update 读-改-写)
	SetThinking("high")
	p = Load()
	if p.Role != "finance" || len(p.Statusline) != 2 || p.Thinking != "high" {
		t.Fatalf("写别的字段抹掉了角色/状态栏: %+v", p)
	}
	// 空 = 回基线:显式清空后 JSON 里不该再留字段(omitempty)
	SetStatusline(nil)
	raw, err := os.ReadFile(filepath.Join(home, "config", "gah-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["statusline"]; ok {
		t.Fatalf("清空后不该留 statusline 字段: %s", raw)
	}
	if Load().Statusline != nil {
		t.Fatalf("清空后应为 nil: %v", Load().Statusline)
	}
	// 空切片等同于 nil
	SetStatusline([]string{})
	if Load().Statusline != nil {
		t.Fatal("空切片应等同 nil")
	}
	// Role 空 = 停用角色
	SetRole("")
	if got := Load().Role; got != "" {
		t.Fatalf("SetRole(\"\") 应清空: %q", got)
	}
}
