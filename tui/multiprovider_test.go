// 多 provider TUI 层单测:modelOptions 聚合编码(provider|model,失败条目不提供选项)、
// /provider 二级枚举(use→现有 provider 名)、providerShortFromURL 与 providerfile 短名一致。
package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/providerfile"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeMultiLLM 最小 LLMService+MultiProviderService 替身(命令层测试用)。
type fakeMultiLLM struct {
	model      string
	lists      []sdk.ProviderModelList
	activeName string
	providers  []sdk.ProviderProfile
}

func (f *fakeMultiLLM) RegisterAdapter(sdk.LLMAdapter) sdk.Disposer { return func() {} }
func (f *fakeMultiLLM) SetModel(m string)                           { f.model = m }
func (f *fakeMultiLLM) Complete(ctx context.Context, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	return nil, errors.New("not used")
}
func (f *fakeMultiLLM) Model() string                            { return f.model }
func (f *fakeMultiLLM) List() []string                           { return nil }
func (f *fakeMultiLLM) SetProvider(baseURL, apiKey string) error { return nil }
func (f *fakeMultiLLM) UnsetProvider(field string) error         { return nil }
func (f *fakeMultiLLM) ResetProvider() error                     { return nil }
func (f *fakeMultiLLM) ProviderInfo() (string, string, bool) {
	for _, p := range f.providers {
		if p.Active {
			return p.BaseURL, p.APIKey, true
		}
	}
	return "", "", false
}
func (f *fakeMultiLLM) ListModels() ([]sdk.ModelInfo, error) { return nil, errors.New("not used") }
func (f *fakeMultiLLM) SetThinking(t sdk.ThinkingLevel)      {}
func (f *fakeMultiLLM) Thinking() sdk.ThinkingLevel          { return 0 }
func (f *fakeMultiLLM) Providers() []sdk.ProviderProfile     { return f.providers }
func (f *fakeMultiLLM) AddProvider(name, baseURL, apiKey, model string) error {
	f.activeName = name
	return nil
}
func (f *fakeMultiLLM) SetActiveProvider(name string) error { f.activeName = name; return nil }
func (f *fakeMultiLLM) RemoveProvider(name string) error {
	for _, p := range f.providers {
		if p.Name == name {
			// 镜像 host-llm:provider.yaml 为单一事实源(删活跃时活跃顺延/删空清文件)
			return providerfile.Remove(name)
		}
	}
	return errString("provider: 不存在 " + name)
}
func (f *fakeMultiLLM) ListAllModels() []sdk.ProviderModelList { return f.lists }

// TestModelOptionsAggregate 聚合:Value = provider|模型(选中后可解析来源);
// 拉取失败 provider 不提供选项(不可达走 /provider show)。
func TestModelOptionsAggregate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	fake := &fakeMultiLLM{lists: []sdk.ProviderModelList{
		{Name: "siliconflow", BaseURL: "https://api.siliconflow.cn/v1", Models: []sdk.ModelInfo{
			{ID: "deepseek-ai/DeepSeek-V3"}, {ID: "Qwen/Qwen2.5-72B"},
		}},
		{Name: "dead", BaseURL: "http://127.0.0.1:1/v1", Err: errors.New("不可达")},
		{Name: "deepseek", BaseURL: "https://api.deepseek.com/v1", Models: []sdk.ModelInfo{
			{ID: "deepseek-chat"},
		}},
	}}
	a := &App{llm: fake, model: &Model{state: &State{}}}
	opts := a.modelOptions(nil)
	if len(opts) != 3 {
		t.Fatalf("应聚合 3 个可用模型(失败 provider 跳过): %+v", opts)
	}
	if opts[0].Value != "siliconflow|deepseek-ai/DeepSeek-V3" {
		t.Fatalf("Value 应携带来源: %q", opts[0].Value)
	}
	got := false
	for _, o := range opts {
		if o.Value == "deepseek|deepseek-chat" {
			got = true
		}
	}
	if !got {
		t.Fatal("应含第二 provider 的模型项")
	}
	for _, o := range opts {
		if strings.Contains(o.Value, "dead") {
			t.Fatal("失败 provider 不应提供选项")
		}
	}
}

// TestProviderLevel2Use 二级枚举:use → 现有 provider 名(来自文件)。
func TestProviderLevel2Use(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := providerfile.Add(providerfile.Provider{Name: "a", BaseURL: "https://a/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := providerfile.Add(providerfile.Provider{Name: "b", BaseURL: "https://b/v1"}); err != nil {
		t.Fatal(err)
	}
	a := &App{}
	opts := a.providerLevel2([]string{"provider", "use"})
	if len(opts) != 2 || opts[0].Value != "a" || opts[1].Value != "b" {
		t.Fatalf("use 二级应枚举 provider: %+v", opts)
	}
	// remove 与 use 同枚举(按名删单条)
	if rm := a.providerLevel2([]string{"provider", "remove"}); len(rm) != 2 || rm[0].Value != "a" {
		t.Fatalf("remove 二级应枚举 provider: %+v", rm)
	}
	// 其它分支不枚举(选中即执行/走自由参数)
	if u := a.providerLevel2([]string{"provider", "unset"}); len(u) != 3 {
		t.Fatalf("unset 二级应三字段: %+v", u)
	}
	if a.providerLevel2([]string{"provider", "clear"}) != nil {
		t.Fatal("clear 无二级")
	}
	// 自由参数:add/set 三序列;其余 nil
	if f := a.providerFree2([]string{"provider", "add"}); len(f) != 3 || f[2] != "model?" {
		t.Fatalf("add 自由序列: %+v", f)
	}
	if a.providerFree2([]string{"provider", "use"}) != nil {
		t.Fatal("use 无自由参数")
	}
}

// TestAppProviderRemove TUI 副本 /provider remove(host-internal-commands 未装配时的回退路径):
// 缺参/不存在显式报错,删非活跃不动活跃,删活跃顺延。
func TestAppProviderRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	for _, p := range []providerfile.Provider{
		{Name: "a", BaseURL: "https://a/v1", APIKey: "k-a", Model: "m-a"},
		{Name: "b", BaseURL: "https://b/v1", APIKey: "k-b", Model: "m-b"},
	} {
		if err := providerfile.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeMultiLLM{providers: []sdk.ProviderProfile{{Name: "a", Active: true}, {Name: "b"}}}
	a := &App{llm: fake, model: &Model{state: &State{}}}
	if _, err := a.cmdProvider([]string{"remove"}); err == nil {
		t.Fatal("缺参应报错")
	}
	if _, err := a.cmdProvider([]string{"remove", "ghost"}); err == nil {
		t.Fatal("删不存在应报错")
	}
	out, err := a.cmdProvider([]string{"remove", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "非活跃") || providerfile.Active() != "a" {
		t.Fatalf("删非活跃: %q active=%q", out, providerfile.Active())
	}
	out, err = a.cmdProvider([]string{"remove", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "最后一个") || providerfile.Active() != "" {
		t.Fatalf("删空: %q active=%q", out, providerfile.Active())
	}
}
