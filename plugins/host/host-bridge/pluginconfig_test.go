// 第八十五批单测:插件自报 ConfigEnv → 宿主**代读**配置(搜索配置)注入子进程 env 的校验与优先级。
//
// 为什么这条链需要钉:默认形态下 web_search 跑在外部插件进程里,而该进程在 macOS 默认沙箱档下
// 被内核凭据读拒挡在 $GAH_HOME/config 之外 ⇒ "写进配置文件"这条通道对插件失效(第八十五批修正)。
// 宿主代读 + 按声明注入是唯一"插件拿得到、模型经 shell 拿不到"的通道,声明校验不能松:
// 声明来自被约束方,未知名必须丢弃。
package hostbridge

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/searchfile"
)

// newCfgTestBridge 只要日志面(本链不碰沙箱/工具面)。
func newCfgTestBridge() *Bridge {
	return &Bridge{lg: slog.New(slog.DiscardHandler)}
}

// writeCfg 在隔离数据根写搜索配置。
func writeCfg(t *testing.T, f searchfile.File) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	if err := searchfile.Save(f); err != nil {
		t.Fatal(err)
	}
}

func hasEnvKV(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

func TestConfigEnvFromCaps(t *testing.T) {
	decl := []string{searchfile.EnvAPIKey, searchfile.EnvEndpoint, searchfile.EnvProvider}

	t.Run("命中白名单:按文件值注入(空字段不注入)", func(t *testing.T) {
		writeCfg(t, searchfile.File{APIKey: "file-key", Endpoint: "http://127.0.0.1:8787"})
		got := newCfgTestBridge().configEnvFromCaps(Capabilities{ConfigEnv: decl}, true)
		if !hasEnvKV(got, searchfile.EnvAPIKey+"=file-key") ||
			!hasEnvKV(got, searchfile.EnvEndpoint+"=http://127.0.0.1:8787") {
			t.Fatalf("应注入 key 与端点,got %v", got)
		}
		if hasEnvKV(got, searchfile.EnvProvider+"=") || len(got) != 2 {
			t.Fatalf("文件里没配 provider 就不该注入,got %v", got)
		}
	})

	t.Run("未知名丢弃且不注入", func(t *testing.T) {
		writeCfg(t, searchfile.File{APIKey: "file-key"})
		got := newCfgTestBridge().configEnvFromCaps(
			Capabilities{ConfigEnv: []string{searchfile.EnvAPIKey, "GAH_TOTALLY_UNKNOWN", "PATH"}}, true)
		if len(got) != 1 || got[0] != searchfile.EnvAPIKey+"=file-key" {
			t.Fatalf("只应注入白名单命中项,got %v", got)
		}
	})

	t.Run("显式点名且有值时不覆盖(GAH_EXT_ENV_PASS 更明确)", func(t *testing.T) {
		writeCfg(t, searchfile.File{APIKey: "file-key"})
		t.Setenv(searchfile.EnvAPIKey, "env-key")
		t.Setenv("GAH_EXT_ENV_PASS", searchfile.EnvAPIKey)
		got := newCfgTestBridge().configEnvFromCaps(Capabilities{ConfigEnv: []string{searchfile.EnvAPIKey}}, true)
		if len(got) != 0 {
			t.Fatalf("点名且有值时不该再注入(避免重复键),got %v", got)
		}
	})

	t.Run("点名但值为空 → 文件值顶上", func(t *testing.T) {
		writeCfg(t, searchfile.File{APIKey: "file-key"})
		t.Setenv(searchfile.EnvAPIKey, "")
		t.Setenv("GAH_EXT_ENV_PASS", searchfile.EnvAPIKey)
		got := newCfgTestBridge().configEnvFromCaps(Capabilities{ConfigEnv: []string{searchfile.EnvAPIKey}}, true)
		if len(got) != 1 || got[0] != searchfile.EnvAPIKey+"=file-key" {
			t.Fatalf("点名但空值时应回落到文件值,got %v", got)
		}
	})

	t.Run("未声明/探测失败不注入", func(t *testing.T) {
		writeCfg(t, searchfile.File{APIKey: "file-key"})
		b := newCfgTestBridge()
		if got := b.configEnvFromCaps(Capabilities{}, true); len(got) != 0 {
			t.Fatalf("未声明不该注入,got %v", got)
		}
		if got := b.configEnvFromCaps(Capabilities{ConfigEnv: decl}, false); len(got) != 0 {
			t.Fatalf("探测失败(未声明)不该注入,got %v", got)
		}
	})

	t.Run("配置文件读不了:跳过注入且不致命", func(t *testing.T) {
		t.Setenv("GAH_HOME", t.TempDir())
		// 目录用 filepath.Dir(不能按 "/" 切路径 —— Windows 上分隔符是 `\\`,切出 -1 直接 panic)。
		if err := os.MkdirAll(filepath.Dir(searchfile.Path()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(searchfile.Path(), []byte("api_key: [坏\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := newCfgTestBridge().configEnvFromCaps(Capabilities{ConfigEnv: decl}, true); len(got) != 0 {
			t.Fatalf("坏配置应跳过注入,got %v", got)
		}
	})
}
