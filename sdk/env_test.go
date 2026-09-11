package sdk

import (
	"os"
	"testing"
)

func TestSanitizedEnvFiltersCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	t.Setenv("GITHUB_TOKEN", "gh-secret")
	t.Setenv("MY_SECRET", "s")
	t.Setenv("GAH_PROFILE", "tui")
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/x")

	env := SanitizedEnv(os.Environ())
	for _, kv := range env {
		k, _, _ := cutKey(kv)
		if k == "OPENAI_API_KEY" || k == "GITHUB_TOKEN" || k == "MY_SECRET" {
			t.Fatalf("凭据键 %s 不应泄漏到工具子进程", k)
		}
	}
	// 基础键保留
	has := map[string]bool{}
	for _, kv := range env {
		k, _, _ := cutKey(kv)
		has[k] = true
	}
	for _, want := range []string{"PATH", "HOME", "GAH_PROFILE"} {
		if !has[want] {
			t.Fatalf("基础键 %s 应保留", want)
		}
	}
}

func envSet(env []string) map[string]bool {
	has := map[string]bool{}
	for _, kv := range env {
		k, _, _ := cutKey(kv)
		has[k] = true
	}
	return has
}

// TestSanitizedEnvExtendedCredentialPatterns 扩展形态:AWS_ 前缀 / _ACCESS_KEY_ID / _PAT / _PRIVATE_KEY / _PASSWD / _KEY 亦须滤除。
func TestSanitizedEnvExtendedCredentialPatterns(t *testing.T) {
	keys := []string{
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"GITHUB_PAT", "FOO_PRIVATE_KEY", "MY_PASSWD", "SERVICE_KEY", "ANTHROPIC_AUTH_TOKEN",
	}
	for _, k := range keys {
		t.Setenv(k, "leak-me")
	}
	t.Setenv("PATH", "/usr/bin")

	has := envSet(SanitizedEnv(os.Environ()))
	for _, k := range keys {
		if has[k] {
			t.Fatalf("凭据键 %s 不应下发子进程", k)
		}
	}
	if !has["PATH"] {
		t.Fatal("基础键 PATH 应保留")
	}
}

// TestSanitizedChildEnv 再派生一级子进程的最小环境:不含凭据与 GAH_CB_*,仅基础键兜底。
func TestSanitizedChildEnv(t *testing.T) {
	t.Setenv("EXA_API_KEY", "k")
	t.Setenv("GAH_CB_ADDR", "127.0.0.1:9")
	t.Setenv("GAH_CB_TOKEN", "tok")
	t.Setenv("GAH_PROFILE", "tui")
	t.Setenv("PATH", "/usr/bin")

	has := envSet(SanitizedChildEnv())
	for _, k := range []string{"EXA_API_KEY", "GAH_CB_ADDR", "GAH_CB_TOKEN", "GAH_PROFILE"} {
		if has[k] {
			t.Fatalf("子进程最小环境不应含 %s", k)
		}
	}
	if !has["PATH"] {
		t.Fatal("基础键 PATH 应兜底保留")
	}
}

func cutKey(kv string) (string, string, bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return kv, "", false
}
