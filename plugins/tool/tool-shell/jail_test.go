// jail_test.go:环境 jail 单测(R10 ①-A)。
//
// 断言的不变量:①缓存根/临时根恒定落在数据根 jail/ 内(且 base 里缺的键被补上,
// 否则 Go/Node/pip 会回落用户家目录);②读配置/凭据用的变量(HOME/GOPATH 等)不被改动;
// ③同一键只出现一次;④jail 建不起来必须显式失败(不许静默放行);⑤真的落到子进程环境里。
package toolshell

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// envValue 取环境切片里 key 的值(dup=true 表示出现多次)。
func envValue(env []string, key string) (value string, dup bool) {
	found := false
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k != key {
			continue
		}
		if found {
			return value, true
		}
		value, found = v, true
	}
	return value, false
}

// TestJailEnvConvergesRoots 缓存根/临时根被覆盖且键被补全;其余变量原样保留;同一键只出现一次。
func TestJailEnvConvergesRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	root := filepath.Join(home, "jail")

	base := []string{
		"HOME=/Users/probe",
		"GOPATH=/Users/probe/go",
		"PATH=/usr/bin",
		"TMPDIR=/var/tmp/orig",                // 已存在 → 被覆盖
		"TMPDIR=/var/tmp/orig",                // 重复 → 只保留一次
		"ANTHROPIC_API_KEY=should-not-matter", // 原样保留(凭据过滤由 sdk.SanitizedEnv 负责)
		"MALFORMED_NO_EQUALS",                 // 形态异常:原样保留,不猜
	}
	env, err := jailEnv(base)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"TMPDIR":           filepath.Join(root, "tmp"),
		"TMP":              filepath.Join(root, "tmp"),
		"TEMP":             filepath.Join(root, "tmp"),
		"XDG_CACHE_HOME":   filepath.Join(root, "cache"),
		"GOCACHE":          filepath.Join(root, "cache", "go-build"),
		"GOMODCACHE":       filepath.Join(root, "cache", "go-mod"),
		"npm_config_cache": filepath.Join(root, "cache", "npm"),
		"PIP_CACHE_DIR":    filepath.Join(root, "cache", "pip"),
	}
	for k, v := range want {
		got, dup := envValue(env, k)
		if dup {
			t.Fatalf("%s 出现多次,jail 环境须去重: %v", k, env)
		}
		if got != v {
			t.Fatalf("%s = %q, want %q", k, got, v)
		}
	}
	// 读配置/凭据用的变量:必须原样保留(jail 不碰它们,否则 git/ssh/gpg 会失效)
	for k, v := range map[string]string{
		"HOME":              "/Users/probe",
		"GOPATH":            "/Users/probe/go",
		"PATH":              "/usr/bin",
		"ANTHROPIC_API_KEY": "should-not-matter",
	} {
		got, _ := envValue(env, k)
		if got != v {
			t.Fatalf("%s = %q, want 原样 %q", k, got, v)
		}
	}
	// 形态异常的条目(无 `=`)原样保留
	kept := false
	for _, kv := range env {
		if kv == "MALFORMED_NO_EQUALS" {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("形态异常的环境条目应原样保留: %v", env)
	}
}

// TestJailEnvDirModeAndTTL jail 目录 0700;tmp 下过期条目被回收,新鲜条目与缓存根保留。
func TestJailEnvDirModeAndTTL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	root := filepath.Join(home, "jail")

	// 先建出目录结构,再塞入"过期/新鲜/缓存"三类样本
	if _, err := jailEnv(nil); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, "tmp", "stale")
	fresh := filepath.Join(root, "tmp", "fresh")
	cached := filepath.Join(root, "cache", "go-build", "keep")
	for _, p := range []string{old, fresh, cached} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(cached, past, past); err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{"tmp", "cache", filepath.Join("cache", "go-build")} {
		info, err := os.Stat(filepath.Join(root, d))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("%s 权限 = %o, want 700", d, perm)
		}
	}

	if _, err := jailEnv(nil); err != nil { // 再次调用触发回收
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("过期 tmp 条目应被回收,stat err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("新鲜 tmp 条目应保留: %v", err)
	}
	if _, err := os.Stat(cached); err != nil {
		t.Fatalf("缓存根不参与回收(清缓存=每次冷启动): %v", err)
	}
}

// TestJailEnvDisabledByEnv GAH_SHELL_JAIL=0:环境原样返回且不建目录。
func TestJailEnvDisabledByEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	t.Setenv("GAH_SHELL_JAIL", "0")

	base := []string{"TMPDIR=/var/tmp/orig", "GOCACHE=/orig/cache"}
	env, err := jailEnv(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(env, "\n") != strings.Join(base, "\n") {
		t.Fatalf("关闭开关后环境应原样返回, got %v", env)
	}
	if _, err := os.Stat(filepath.Join(home, "jail")); !os.IsNotExist(err) {
		t.Fatalf("关闭开关后不应建 jail 目录, stat err=%v", err)
	}
}

// TestJailEnvFailsWhenRootUnusable 数据根不可用(GAH_HOME 指向普通文件)→ 显式报错,不静默放行。
func TestJailEnvFailsWhenRootUnusable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAH_HOME", file)

	if _, err := jailEnv([]string{"TMPDIR=/tmp"}); err == nil {
		t.Fatal("jail 建不起来必须显式失败")
	} else if !strings.Contains(err.Error(), "jail") {
		t.Fatalf("报错应指明 jail 目录, got %v", err)
	}
}

// TestShellToolJailAppliesToChild 环境真的落到子进程:子进程看到的 TMPDIR/GOCACHE 在 jail 内。
func TestShellToolJailAppliesToChild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	root := filepath.Join(home, "jail")

	out := runShell(t, `printf '%s;%s' "$TMPDIR" "$GOCACHE"`)
	if !strings.HasPrefix(out, root+string(filepath.Separator)) {
		t.Fatalf("子进程 TMPDIR 应在 jail 内, got %q (root=%s)", out, root)
	}
	tmp, cache, ok := strings.Cut(out, ";")
	if !ok {
		t.Fatalf("输出格式不符: %q", out)
	}
	if !strings.HasPrefix(cache, filepath.Join(root, "cache", "go-build")) {
		t.Fatalf("子进程 GOCACHE 应在 jail 内, got %q", cache)
	}
	if tmp == "" || cache == "" {
		t.Fatalf("两个变量都必须可见: %q", out)
	}
}

// TestShellToolJailKeepsCredentialIsolation jail 覆盖在后不得让凭据"复活"。
func TestShellToolJailKeepsCredentialIsolation(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Setenv("GAH_PROBE_API_KEY", "sekret")
	out := runShell(t, `printf '%s' "${GAH_PROBE_API_KEY:-absent}"`)
	if strings.Contains(out, "sekret") {
		t.Fatalf("sanitize 之后 jail 不得恢复凭据: %q", out)
	}
	if !strings.Contains(out, "absent") {
		t.Fatalf("子进程不应看到凭据键, got %q", out)
	}
}

// TestShellToolJailDisabledReachesChild 关闭开关后子进程看到原环境(证明开关真的生效)。
func TestShellToolJailDisabledReachesChild(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Setenv("GAH_SHELL_JAIL", "0")
	t.Setenv("TMPDIR", "/var/tmp/probe") // 关闭态下必须原样下传
	out := runShell(t, `printf '%s' "$TMPDIR"`)
	if out != "/var/tmp/probe" {
		t.Fatalf("关闭 jail 后子进程 TMPDIR = %q, want /var/tmp/probe", out)
	}
}

// TestShellToolJailFailureIsStructured jail 失败:结构化错误回传模型(不 panic、不返回 Go error)。
func TestShellToolJailFailureIsStructured(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAH_HOME", file)

	raw, _ := json.Marshal(args{Command: "echo should-not-run"})
	res, err := (&ShellTool{timeout: 10 * time.Second}).Execute(context.Background(), string(raw))
	if err != nil {
		t.Fatalf("结构化错误模型:不应返回 Go error, got %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("结果形态应为 map, got %T", res)
	}
	msg, _ := m["error"].(string)
	if !strings.Contains(msg, "jail") {
		t.Fatalf("错误应指明 jail 初始化失败, got %v", m)
	}
	if _, has := m["output"]; has {
		t.Fatalf("jail 失败时不应执行命令: %v", m)
	}
}

// TestExecPtyJailFailure pty 路径同样在 jail 不可用时显式失败(不启动子进程)。
func TestExecPtyJailFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAH_HOME", file)

	if _, _, err := execPty(context.Background(), "echo hi", ""); err == nil {
		t.Fatal("pty 路径 jail 不可用应报错")
	} else if !strings.Contains(err.Error(), "jail") {
		t.Fatalf("报错应指明 jail, got %v", err)
	}
}

// runShell 执行一次 shell 工具并返回子进程输出(测试辅助:只用于 jail 断言)。
func runShell(t *testing.T, command string) string {
	t.Helper()
	raw, err := json.Marshal(args{Command: command})
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&ShellTool{timeout: 30 * time.Second}).Execute(context.Background(), string(raw))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("结果形态应为 map, got %T", res)
	}
	if e, has := m["error"]; has {
		t.Fatalf("命令执行失败: %v", e)
	}
	if e, has := m["exit_error"]; has {
		t.Fatalf("命令退出非零: %v (output=%v)", e, m["output"])
	}
	out, _ := m["output"].(string)
	return out
}
