package searchfile

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
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
	// Windows 走 ACL,`0600` 不产生私密语义(Go 写出的仍是 0666)—— 与 AGENTS.md 跨平台纪律③ 同类,
	// 故用 testutil.PosixPerm() 闸住(与 internal/providerfile 同口径)。
	if testutil.PosixPerm() {
		fi, err := os.Stat(Path())
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("文件权限 = %o,期望 600", perm)
		}
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// 首行是版本标记,第二行才是人读的文件头 —— 断言两者都在(版本标记是迁移的依据)。
	if !strings.HasPrefix(string(raw), configVersionMarker+" "+strconv.Itoa(ConfigVersion)) {
		t.Fatalf("缺版本标记(首行应为 %s %d):\n%s", configVersionMarker, ConfigVersion, raw)
	}
	if !strings.Contains(string(raw), "# gah 联网搜索配置") {
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
	// 排序后:ANYSEARCH_API_KEY < ANYSEARCH_ENDPOINT < EXA_API_KEY < EXA_ENDPOINT <
	//         GAH_SEARCH_API_KEY < GAH_SEARCH_ENDPOINT < GAH_SEARCH_PROVIDER
	want := []string{
		EnvAnysearchAPIKey, EnvAnysearchEndpoint,
		EnvExaAPIKey, EnvExaEndpoint,
		EnvAPIKey, EnvEndpoint, EnvProvider,
	}
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

// TestEndpointForPerProvider endpoint 按 provider 分家。
//
// 为何必须分:通用 endpoint 留着 Exa 的地址、provider 却已是 anysearch 时,请求会
// **静默地** POST 到 Exa —— 两边都不报错,用户只看到"搜不到东西"(2026-10-03 实测踩到)。
func TestEndpointForPerProvider(t *testing.T) {
	f := File{
		Endpoint:          "http://127.0.0.1:8787/generic",
		ExaEndpoint:       "http://127.0.0.1:8787/exa",
		AnysearchEndpoint: "http://127.0.0.1:8787/any",
	}
	if got := f.EndpointFor(ProviderExa); got != "http://127.0.0.1:8787/exa" {
		t.Fatalf("exa 端点应取专属值: %q", got)
	}
	if got := f.EndpointFor(ProviderAnysearch); got != "http://127.0.0.1:8787/any" {
		t.Fatalf("anysearch 端点应取专属值: %q", got)
	}
	// 没有专属值 → 回落通用;都没有 → 空(调用方用官方端点)。
	g := File{Endpoint: "http://127.0.0.1:8787/generic"}
	if got := g.EndpointFor(ProviderExa); got != "http://127.0.0.1:8787/generic" {
		t.Fatalf("无专属值应回落通用: %q", got)
	}
	if got := (File{}).EndpointFor(ProviderExa); got != "" {
		t.Fatalf("无任何配置应为空: %q", got)
	}
	// 未知 provider 不猜(与 KeyFor 同纪律)。
	if got := f.EndpointFor("bing"); got != "http://127.0.0.1:8787/generic" {
		t.Fatalf("未知 provider 应回落通用而非报错: %q", got)
	}
}

// TestKeyForPerProvider key 同样按 provider 分(专属 > 通用;未知 provider 回落通用)。
func TestKeyForPerProvider(t *testing.T) {
	f := File{APIKey: "generic", ExaAPIKey: "exa-key", AnysearchAPIKey: "any-key"}
	if got := f.KeyFor(ProviderExa); got != "exa-key" {
		t.Fatalf("exa key = %q", got)
	}
	if got := f.KeyFor(ProviderAnysearch); got != "any-key" {
		t.Fatalf("anysearch key = %q", got)
	}
	if got := (File{APIKey: "generic"}).KeyFor(ProviderExa); got != "generic" {
		t.Fatalf("无专属 key 应回落通用: %q", got)
	}
	if got := (File{}).KeyFor(ProviderAnysearch); got != "" {
		t.Fatalf("无配置应为空(匿名): %q", got)
	}
}

// TestSaveLoadEndpointRoundTrip 新键落盘/读回不丢(换 provider 不丢对方的配置)。
func TestSaveLoadEndpointRoundTrip(t *testing.T) {
	setupHome(t)
	in := File{
		Provider:          ProviderAnysearch,
		AnysearchAPIKey:   "any-key",
		AnysearchEndpoint: "http://127.0.0.1:1/any",
		ExaAPIKey:         "exa-key",
		ExaEndpoint:       "http://127.0.0.1:1/exa",
	}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != in {
		t.Fatalf("往返后不一致:\n got %+v\nwant %+v", got, in)
	}
	// 切 provider 之后各自的值互不串味
	if g := got.EndpointFor(ProviderExa); g != in.ExaEndpoint {
		t.Fatalf("切到 exa 后端点串味: %q", g)
	}
	if g := got.KeyFor(ProviderAnysearch); g != in.AnysearchAPIKey {
		t.Fatalf("切到 anysearch 后 key 串味: %q", g)
	}
}

// —— schema 迁移(2026-10-03)——

// writeRaw 直接写原始内容(模拟用户手写的老文件;不经 Save 就没有版本标记)。
func writeRaw(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// readRaw 读回文件正文。
func readRaw(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// backupFiles 备份目录里的文件数。
func backupFiles(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(Path()), BackupDirName))
	if err != nil {
		return 0
	}
	return len(entries)
}

// TestMigrateLegacyExaFile 老文件(只有 provider/api_key/endpoint 三项)自动补齐:
// 通用值搬进 exa 专属键、provider 写实、旧版进备份目录、文件拿到版本标记。
func TestMigrateLegacyExaFile(t *testing.T) {
	setupHome(t)
	writeRaw(t, "provider: exa\napi_key: k-123\nendpoint: http://127.0.0.1:8787\n")
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// 值一字不丢,只是换了键
	if got.ExaAPIKey != "k-123" || got.ExaEndpoint != "http://127.0.0.1:8787" {
		t.Fatalf("迁移后值不符: %+v", got)
	}
	if got.APIKey != "" || got.Endpoint != "" {
		t.Fatalf("通用值应被搬走(否则日后切 provider 会把它带过去): %+v", got)
	}
	// 盘上也是新形
	raw := readRaw(t)
	if configVersion([]byte(raw)) != ConfigVersion {
		t.Fatalf("回写后仍无版本标记:\n%s", raw)
	}
	if strings.Contains(raw, "\napi_key:") || strings.Contains(raw, "\nendpoint:") {
		t.Fatalf("回写后仍留通用键:\n%s", raw)
	}
	if !strings.Contains(raw, "exa_api_key: k-123") {
		t.Fatalf("回写后应落在 exa_api_key:\n%s", raw)
	}
	if backupFiles(t) != 1 {
		t.Fatalf("旧版应备份一份,got %d", backupFiles(t))
	}
}

// TestMigrateUnattributedKeepsValues 归属不明(provider 已是非 exa 却留着通用值)时
// **原样保留并提示** —— 自动搬就是猜,猜错的表现是搜索静默失效。
func TestMigrateUnattributedKeepsValues(t *testing.T) {
	setupHome(t)
	writeRaw(t, "provider: anysearch\napi_key: k-123\nendpoint: https://api.exa.ai/search\n")
	got, notes := func() (File, []string) {
		f, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		return f, nil
	}()
	_ = notes
	if got.APIKey != "k-123" || got.Endpoint != "https://api.exa.ai/search" {
		t.Fatalf("归属不明时应原样保留(行为与升级前一致): %+v", got)
	}
	if got.AnysearchAPIKey != "" || got.ExaAPIKey != "" {
		t.Fatalf("不应擅自搬动: %+v", got)
	}
}

// TestMigrateIsIdempotent 迁移只做一次:第二次 Load 不再备份、不再改盘。
func TestMigrateIsIdempotent(t *testing.T) {
	setupHome(t)
	writeRaw(t, "provider: exa\napi_key: k-123\n")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	first := readRaw(t)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	if second := readRaw(t); second != first {
		t.Fatalf("二次 Load 不该再改盘:\n%s\n---\n%s", first, second)
	}
	if n := backupFiles(t); n != 1 {
		t.Fatalf("只该备份一次,got %d", n)
	}
}

// TestMigrateNewerVersionUntouched 盘上版本高于当前 → 原样读,不覆盖用户/未来版本的文件。
func TestMigrateNewerVersionUntouched(t *testing.T) {
	setupHome(t)
	body := configVersionMarker + " 999\nprovider: exa\napi_key: k-123\n"
	writeRaw(t, body)
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "k-123" {
		t.Fatalf("新版本文件不该被改动: %+v", got)
	}
	if readRaw(t) != body {
		t.Fatal("新版本文件不该被回写")
	}
	if backupFiles(t) != 0 {
		t.Fatal("不该为新版本文件建备份")
	}
}

// TestMigrateWithoutProviderWritesDefault 没写 provider 的老文件:补成当前缺省,
// 值按「v1 只有 exa」归到 exa —— anysearch 于是匿名可用(不因升级而坏)。
func TestMigrateWithoutProviderWritesDefault(t *testing.T) {
	setupHome(t)
	writeRaw(t, "api_key: k-123\n")
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != DefaultProvider {
		t.Fatalf("provider 应写实为缺省 %q: %+v", DefaultProvider, got)
	}
	if got.ExaAPIKey != "k-123" {
		t.Fatalf("v1 的通用 key 必属 exa: %+v", got)
	}
	if got.AnysearchAPIKey != "" {
		t.Fatalf("不能把 exa 的 key 塞给 anysearch(会 401): %+v", got)
	}
}

// TestMigrateAlreadySpecificNotTouched 已是新形(有专属键)的文件:迁移不碰专属值。
func TestMigrateAlreadySpecificNotTouched(t *testing.T) {
	setupHome(t)
	writeRaw(t, "provider: anysearch\nanysearch_api_key: any-key\nexa_api_key: exa-key\n")
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.AnysearchAPIKey != "any-key" || got.ExaAPIKey != "exa-key" {
		t.Fatalf("专属键不该被动: %+v", got)
	}
}

// TestMigrateBackupIsPrivate 备份含第三方 key → 权限必须也是 0600(别比原文件松)。
func TestMigrateBackupIsPrivate(t *testing.T) {
	setupHome(t)
	writeRaw(t, "provider: exa\napi_key: k-123\n")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(Path()), BackupDirName)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("备份目录异常: %v %v", entries, err)
	}
	if testutil.PosixPerm() {
		fi, err := os.Stat(filepath.Join(dir, entries[0].Name()))
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("备份权限 = %o,期望 600(含第三方 key,不可比原文件松)", perm)
		}
	}
}
