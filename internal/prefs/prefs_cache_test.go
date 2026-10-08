// prefs 只读侧缓存的护栏(批三)。
//
// 为什么这组测试比看起来重要:缓存错一次 = **界面上报的偏好是旧值**。这类缺陷不会崩,
// 只会让用户「明明改了却看着没改」—— 所以每条口径都要钉死,包括反证。
package prefs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeState 直接写一份 gah-state.json(绕开 Save,模拟**另一个进程**写盘)。
func writeState(t *testing.T, home, body string) {
	t.Helper()
	p := filepath.Join(home, "config", "gah-state.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestLoadCacheHitSameFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	writeState(t, home, `{"thinking":"medium"}`)

	if got := Load().Thinking; got != "medium" {
		t.Fatalf("首次 Load = %q, want medium", got)
	}
	if got := Load().Thinking; got != "medium" {
		t.Fatalf("二次 Load = %q, want medium(应命中缓存且值一致)", got)
	}
}

// 反证:同进程写盘后必须立刻生效(不靠 mtime 分辨率)。
func TestLoadCacheInvalidatedByOwnSave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	writeState(t, home, `{"thinking":"low"}`)
	if got := Load().Thinking; got != "low" {
		t.Fatalf("首次 = %q", got)
	}
	// 紧接着写(同一 tick 内,mtime 可能完全相同)—— 缓存若没被主动清,这里就会读回旧值。
	Save(Prefs{Thinking: "high"})
	if got := Load().Thinking; got != "high" {
		t.Fatalf("Save 后 Load = %q, want high(缓存未失效)", got)
	}
}

// 反证:跨进程写(外部直接改文件)必须生效 —— 桌面壳与 CLI 各起一个实例是常态。
func TestLoadCacheSeesForeignWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	writeState(t, home, `{"thinking":"low"}`)
	if got := Load().Thinking; got != "low" {
		t.Fatalf("首次 = %q", got)
	}
	// 换个进程写:强制 mtime 变化(避免依赖文件系统的时间精度)。
	p := filepath.Join(home, "config", "gah-state.json")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	writeState(t, home, `{"thinking":"high"}`)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if got := Load().Thinking; got != "high" {
		t.Fatalf("外部改盘后 Load = %q, want high(缓存没看见跨进程写)", got)
	}
}

// 反证:主文件从「有」变「无」时不能继续报缓存里的旧值。
func TestLoadCacheRecoversWhenFileRemoved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	writeState(t, home, `{"thinking":"medium"}`)
	if got := Load().Thinking; got != "medium" {
		t.Fatalf("首次 = %q", got)
	}
	if err := os.Remove(filepath.Join(home, "config", "gah-state.json")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := Load().Thinking; got != "" {
		t.Fatalf("删文件后 Load = %q, want 空(不能报缓存里的旧值)", got)
	}
}

// 反证:坏 JSON 不得因为缓存而「复活」成旧值(读不出来 = 零值,与首次一致)。
func TestLoadCacheCorruptFileReturnsZero(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	writeState(t, home, `{"thinking":"medium"}`)
	if got := Load().Thinking; got != "medium" {
		t.Fatalf("首次 = %q", got)
	}
	writeState(t, home, `{ this is not json`)
	if got := Load().Thinking; got != "" {
		t.Fatalf("坏文件后 Load = %q, want 空", got)
	}
}

// GAH_HOME 在进程内被改(测试里常见)时,缓存不得串味到另一个数据根。
func TestLoadCacheKeyedByPath(t *testing.T) {
	h1, h2 := t.TempDir(), t.TempDir()
	writeState(t, h1, `{"thinking":"medium"}`)
	writeState(t, h2, `{"thinking":"high"}`)

	t.Setenv("GAH_HOME", h1)
	if got := Load().Thinking; got != "medium" {
		t.Fatalf("home1 = %q", got)
	}
	t.Setenv("GAH_HOME", h2)
	if got := Load().Thinking; got != "high" {
		t.Fatalf("home2 = %q, want high(缓存串味了)", got)
	}
	t.Setenv("GAH_HOME", h1)
	if got := Load().Thinking; got != "medium" {
		t.Fatalf("回 home1 = %q, want medium", got)
	}
}

// 写路径必须永远拿盘上真值(Update 走 loadFrom 直读,不经只读缓存)。
func TestUpdateNotServedFromReadCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	writeState(t, home, `{"thinking":"low","sandbox":"read-only"}`)
	if got := Load().Thinking; got != "low" {
		t.Fatalf("预热 = %q", got)
	}
	Update(func(p *Prefs) { p.Thinking = "high" })
	got := Load()
	if got.Thinking != "high" || got.Sandbox != "read-only" {
		t.Fatalf("Update 后 = %+v, want high + 保留 read-only", got)
	}
}
