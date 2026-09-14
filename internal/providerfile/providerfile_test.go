// provider.yaml v2 持久化单测:多 provider/active/迁移兼容/0600/坏文件/逐字段删。GAH_HOME 隔离。
package providerfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
)

func TestAddSaveLoadAndPerm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)

	// 首条 Add:name 空自动域短名 + 自动 active
	if err := Add(Provider{BaseURL: "https://api.siliconflow.cn/v1", APIKey: "sk-test-123456", Model: "deepseek-ai/DeepSeek-V3"}); err != nil {
		t.Fatal(err)
	}
	path := Path()
	if !strings.HasPrefix(path, home) { // 段边界由 GAH_HOME 拼接保证;filepath.HasPrefix 已弃用
		t.Fatalf("路径应在 GAH_HOME 下: %s", path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if testutil.PosixPerm() && fi.Mode().Perm() != 0o600 {
		t.Fatalf("provider.yaml 权限应为 0600, got %o", fi.Mode().Perm())
	}
	f, err := LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Providers) != 1 || f.Providers[0].Name != "siliconflow" || f.Active != "siliconflow" {
		t.Fatalf("首条自动激活: %+v", f)
	}
	if f.Providers[0].APIKey != "sk-test-123456" || f.Providers[0].Model != "deepseek-ai/DeepSeek-V3" {
		t.Fatalf("字段往返: %+v", f.Providers[0])
	}

	// 第二条并存:active 不变(仍 siliconflow)
	if err := Add(Provider{BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-ds", Model: "deepseek-chat"}); err != nil {
		t.Fatal(err)
	}
	f, _ = LoadFile()
	if len(f.Providers) != 2 || f.Active != "siliconflow" {
		t.Fatalf("新增不应夺活跃: %+v", f)
	}
	// Load() 返回活跃 provider
	p, err := Load()
	if err != nil || p.Name != "siliconflow" {
		t.Fatalf("Load 应取活跃: %+v %v", p, err)
	}

	// 同名 upsert:更新并激活(非空覆盖,空保留旧)
	if err := Add(Provider{BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-ds2"}); err != nil {
		t.Fatal(err)
	}
	f, _ = LoadFile()
	if f.Active != "deepseek" || f.Providers[1].APIKey != "sk-ds2" || f.Providers[1].Model != "deepseek-chat" {
		t.Fatalf("同名更新应激活且空 model 保留: %+v", f)
	}

	// Clear 后文件删除;缺文件 Load 空不报错;重复 Clear 幂等
	if err := Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Clear 后文件应删除")
	}
	if p, err := Load(); err != nil || p != (Provider{}) {
		t.Fatalf("缺文件应空: %+v %v", p, err)
	}
	if err := Clear(); err != nil {
		t.Fatal("重复 Clear 应幂等")
	}
}

func TestLegacyMigration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("base_url: https://api.siliconflow.cn/v1\napi_key: sk-old\nmodel: old-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 兼容读取:迁移为单 provider(name=域短名),active 指向它;不改盘
	f, err := LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Providers) != 1 || f.Providers[0].Name != "siliconflow" ||
		f.Providers[0].APIKey != "sk-old" || f.Providers[0].Model != "old-model" || f.Active != "siliconflow" {
		t.Fatalf("旧格式迁移视图: %+v", f)
	}
	// 旧 reader Load 等值
	if p, err := Load(); err != nil || p.BaseURL != "https://api.siliconflow.cn/v1" || p.Model != "old-model" {
		t.Fatalf("旧格式 Load: %+v %v", p, err)
	}
	// UpdateModel 联动活跃(迁移单 provider)= 旧行为等价(只改 model 保留其余)
	if err := UpdateModel("deepseek-ai/DeepSeek-V3"); err != nil {
		t.Fatal(err)
	}
	f, _ = LoadFile()
	if f.Providers[0].Model != "deepseek-ai/DeepSeek-V3" || f.Providers[0].APIKey != "sk-old" {
		t.Fatalf("UpdateModel 只改 model: %+v", f.Providers[0])
	}
}

func TestUnsetActiveFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	Add(Provider{BaseURL: "https://api.siliconflow.cn/v1", APIKey: "sk-123456", Model: "m1"})
	Add(Provider{BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-ds"})

	// 删活跃(siliconflow)字段;SetActive 切到 deepseek 再删 api_key
	if err := SetActive("deepseek"); err != nil {
		t.Fatal(err)
	}
	if err := Unset("api_key"); err != nil {
		t.Fatal(err)
	}
	p, _ := Load()
	if p.APIKey != "" || p.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("删字段保留其余: %+v", p)
	}
	// 删活跃最后字段 → 整个移除,active 重置为剩余首个(siliconflow)
	if err := Unset("base_url"); err != nil {
		t.Fatal(err)
	}
	f, _ := LoadFile()
	if len(f.Providers) != 1 || f.Active != "siliconflow" || f.Providers[0].Name != "siliconflow" {
		t.Fatalf("删空应移除并重置 active: %+v", f)
	}
	// 删最后一个 provider 的全部字段 → 文件删除
	if err := Unset("model"); err != nil {
		t.Fatal(err)
	}
	if err := Unset("api_key"); err != nil {
		t.Fatal(err)
	}
	if err := Unset("base_url"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path()); !os.IsNotExist(err) {
		t.Fatal("最后一个 provider 删空应删文件")
	}
	// 未知字段显式报错;缺文件 no-op
	if err := Unset("apiKey"); err == nil {
		t.Fatal("未知字段应报错")
	}
	if err := Unset("api_key"); err != nil {
		t.Fatalf("缺文件应 no-op: %v", err)
	}
}

func TestUpdateModelKeepsOthersAndNoop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := UpdateModel("gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path()); !os.IsNotExist(err) {
		t.Fatal("无 provider 不应写文件")
	}
	Add(Provider{BaseURL: "https://api.siliconflow.cn/v1", APIKey: "sk-123456", Model: "old"})
	if err := UpdateModel("deepseek-ai/DeepSeek-V3"); err != nil {
		t.Fatal(err)
	}
	p, _ := Load()
	if p.Model != "deepseek-ai/DeepSeek-V3" || p.BaseURL != "https://api.siliconflow.cn/v1" || p.APIKey != "sk-123456" {
		t.Fatalf("UpdateModel 应只改 model: %+v", p)
	}
}

func TestLoadMalformedAndEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("base_url: [broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(); err == nil {
		t.Fatal("坏 yaml 应显式报错")
	}
	os.Remove(Path())
	if err := os.WriteFile(Path(), []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFile()
	if err != nil || len(f.Providers) != 0 {
		t.Fatalf("空文件应空 File: %+v %v", f, err)
	}
}

func TestSetActiveNotFoundAndRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	Add(Provider{Name: "a", BaseURL: "https://a/v1"})
	Add(Provider{Name: "b", BaseURL: "https://b/v1"})
	if err := SetActive("zzz"); err == nil {
		t.Fatal("切换不存在应报错")
	}
	if err := Remove("a"); err != nil {
		t.Fatal(err)
	}
	// a 是 active → 重置为剩余首个 b
	if f, _ := LoadFile(); f.Active != "b" || len(f.Providers) != 1 {
		t.Fatalf("删活跃重置: %+v", f)
	}
	if err := Remove("zzz"); err != nil {
		t.Fatalf("删不存在应 no-op: %v", err)
	}
}

func TestShortNameOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://api.siliconflow.cn/v1", "siliconflow"},
		{"https://api.deepseek.com/v1", "deepseek"},
		{"http://localhost:8899/v1", "localhost"},
		{"api.x.com", "api.x.com"}, // 无协议:解析无 host → 原文回退
		{"", "default"},
		{"not a url", "not a url"},
	}
	for _, c := range cases {
		if got := ShortNameOf(c.in); got != c.want {
			t.Fatalf("ShortNameOf(%q) = %q,期望 %q", c.in, got, c.want)
		}
	}
}

// TestSetFieldsPartial SetFields 局部更新:name 缺省 = 活跃;非空覆盖、空保留。
func TestSetFieldsPartial(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	Add(Provider{BaseURL: "https://api.siliconflow.cn/v1", APIKey: "sk-old", Model: "m-old"})
	if err := SetFields("", "https://api.siliconflow.cn/v2", "sk-new", ""); err != nil {
		t.Fatal(err)
	}
	p, _ := Load()
	if p.BaseURL != "https://api.siliconflow.cn/v2" || p.APIKey != "sk-new" || p.Model != "m-old" {
		t.Fatalf("局部更新: %+v", p)
	}
}
