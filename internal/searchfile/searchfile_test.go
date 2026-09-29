package searchfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupHome 把数据根指到临时目录(Path() 经 sdk.Home() 派生)。
func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	return home
}

func TestPathUnderDataRoot(t *testing.T) {
	home := setupHome(t)
	want := filepath.Join(home, "config", "search.yaml")
	if got := Path(); got != want {
		t.Fatalf("Path() = %q,期望 %q", got, want)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	setupHome(t)
	in := File{Provider: "exa", APIKey: "k-123", Endpoint: "http://127.0.0.1:8787"}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 文件权限 0600(内含第三方 key;备份/dir 权限面见 C2-b 同一纪律)。
	fi, err := os.Stat(Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("文件权限 = %o,期望 600", perm)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(string(raw), "# gah 联网搜索配置") {
		t.Fatalf("缺文件头注释:\n%s", raw)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != in {
		t.Fatalf("往返不一致:got %+v,期望 %+v", got, in)
	}
}

func TestSaveOmitsEmptyAndClearsFields(t *testing.T) {
	setupHome(t)
	if err := Save(File{APIKey: "k", Endpoint: "http://x"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 清空即删除该项:再次保存空 File,文件里不应残留 key。
	if err := Save(File{}); err != nil {
		t.Fatalf("Save(空): %v", err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// 只看 yaml 正文(文件头注释里本身就写着 api_key/endpoint 两个词)。
	var body []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		body = append(body, line)
	}
	joined := strings.Join(body, "\n")
	if strings.Contains(joined, "api_key") || strings.Contains(joined, "endpoint") {
		t.Fatalf("空字段应不落盘:\n%s", raw)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != (File{}) {
		t.Fatalf("期望零值,got %+v", got)
	}
}

func TestLoadMissingAndBadYAML(t *testing.T) {
	setupHome(t)
	if f, err := Load(); err != nil || f != (File{}) {
		t.Fatalf("缺文件应返回零值且不报错:got %+v err %v", f, err)
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("api_key: [未闭合\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "解析失败") {
		t.Fatalf("坏 yaml 应显式报错,got %v", err)
	}
}

func TestKnownEnvKeysSorted(t *testing.T) {
	got := KnownEnvKeys()
	want := []string{EnvAPIKey, EnvEndpoint, EnvProvider} // 排序后:EXA_ < GAH_SEARCH_ENDPOINT < GAH_SEARCH_PROVIDER
	if len(got) != len(want) {
		t.Fatalf("KnownEnvKeys 数量 = %d,期望 %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("KnownEnvKeys() = %v,期望 %v", got, want)
		}
	}
}

func TestEnvValuesSkipsEmpty(t *testing.T) {
	got := EnvValues(File{APIKey: "k"})
	if len(got) != 1 || got[EnvAPIKey] != "k" {
		t.Fatalf("只应有 api_key 注入项,got %v", got)
	}
	if len(EnvValues(File{})) != 0 {
		t.Fatal("零值配置不应产生注入项")
	}
}

// TestResolvePriority 四条分支:env 全备免读文件 / env 覆盖文件 / env 命中时文件错误不致命 /
// 无 env 时文件错误响亮。
func TestResolvePriority(t *testing.T) {
	t.Run("env 三项齐备则不读文件(坏文件也不影响)", func(t *testing.T) {
		setupHome(t)
		if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(Path(), []byte("api_key: [坏\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(EnvAPIKey, "env-key")
		t.Setenv(EnvProvider, "exa")
		t.Setenv(EnvEndpoint, "http://env")
		got, err := Resolve()
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.APIKey != "env-key" || got.Provider != "exa" || got.Endpoint != "http://env" {
			t.Fatalf("应全取 env,got %+v", got)
		}
	})

	t.Run("env 优先且只补缺", func(t *testing.T) {
		setupHome(t)
		if err := Save(File{APIKey: "file-key", Provider: "file-provider", Endpoint: "http://file"}); err != nil {
			t.Fatal(err)
		}
		t.Setenv(EnvAPIKey, "env-key")
		got, err := Resolve()
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.APIKey != "env-key" || got.Provider != "file-provider" || got.Endpoint != "http://file" {
			t.Fatalf("env 应覆盖 key、其余取文件,got %+v", got)
		}
	})

	t.Run("env 命中时文件读错不致命(外部插件沙箱读拒不炸)", func(t *testing.T) {
		setupHome(t)
		if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(Path(), []byte("api_key: [坏\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(EnvAPIKey, "env-key")
		got, err := Resolve()
		if err != nil {
			t.Fatalf("env 命中时不应上抛文件错误:%v", err)
		}
		if got.APIKey != "env-key" || got.Provider != "" || got.Endpoint != "" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("无 env 时文件读错响亮", func(t *testing.T) {
		setupHome(t)
		if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(Path(), []byte("api_key: [坏\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(); err == nil {
			t.Fatal("无任何 env 命中时坏配置应报错")
		}
	})
}
