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

func cutKey(kv string) (string, string, bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return kv, "", false
}
