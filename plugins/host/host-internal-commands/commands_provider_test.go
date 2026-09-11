// 命令执行补测(覆盖率补强):/provider 全分支(多 provider 桩 + 真 providerfile 落盘)、
// /model 聚合枚举与持久化联动、/plugins 持久开关与 profile 注入、/workspace 路径解析与
// 会话切换、以及各命令的选择器选项构造(specs 内的动态枚举闭包)。
package hostintcmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/install"
	"github.com/nekoleamo/go-agent-harness/internal/providerfile"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubMultiLLM 多 provider 桩:内嵌 LLMService 兜底(未实现方法 nil panic),
// AddProvider 同步落 provider.yaml(与 host-llm 语义一致,便于断言持久化联动)。
type stubMultiLLM struct {
	sdk.LLMService
	mu              sync.Mutex
	providers       []sdk.ProviderProfile
	models          []sdk.ProviderModelList
	listErr         error
	list            []sdk.ModelInfo
	base            string
	key             string
	infoOK          bool
	curModel        string
	active          string
	unset           []string
	reset           bool
	addErr          error
	useErr          error
	addNameOverride string // 非空 = AddProvider 用此名落盘(模拟名称归一化)
}

func (s *stubMultiLLM) Providers() []sdk.ProviderProfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.providers
}

func (s *stubMultiLLM) AddProvider(name, base, key, model string) error {
	if s.addErr != nil {
		return s.addErr
	}
	if s.addNameOverride != "" {
		name = s.addNameOverride // 模拟运行期服务对名字做归一化(与 ShortNameOf 不一致)
	}
	if err := providerfile.Add(providerfile.Provider{Name: name, BaseURL: base, APIKey: key, Model: model}); err != nil {
		return err
	}
	if name == "" {
		name = providerfile.ShortNameOf(base)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.providers {
		if s.providers[i].Name == name {
			s.providers[i] = sdk.ProviderProfile{Name: name, BaseURL: base, APIKey: key, Model: model, Active: s.providers[i].Active || providerfile.Active() == name}
			return nil
		}
	}
	s.providers = append(s.providers, sdk.ProviderProfile{
		Name: name, BaseURL: base, APIKey: key, Model: model, Active: providerfile.Active() == name,
	})
	return nil
}

func (s *stubMultiLLM) SetActiveProvider(name string) error {
	if s.useErr != nil {
		return s.useErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = name
	found := false
	for i := range s.providers {
		s.providers[i].Active = s.providers[i].Name == name
		if s.providers[i].Name == name {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("provider: 不存在 %q", name)
	}
	return nil
}

func (s *stubMultiLLM) ListAllModels() []sdk.ProviderModelList { return s.models }

func (s *stubMultiLLM) ListModels() ([]sdk.ModelInfo, error) { return s.list, s.listErr }

func (s *stubMultiLLM) ProviderInfo() (string, string, bool) { return s.base, s.key, s.infoOK }

func (s *stubMultiLLM) SetModel(m string) { s.curModel = m }

func (s *stubMultiLLM) Model() string { return s.curModel }

func (s *stubMultiLLM) UnsetProvider(field string) error {
	s.unset = append(s.unset, field)
	return nil
}

func (s *stubMultiLLM) ResetProvider() error { s.reset = true; return nil }

func (s *stubMultiLLM) SetThinking(sdk.ThinkingLevel) {}

// stubLLMPlain 仅实现 LLMService(不实现 MultiProviderService):走显式拒绝分支。
type stubLLMPlain struct{ sdk.LLMService }

// stubMgr 插件管理桩。
type stubMgr struct {
	infos    []sdk.PluginInfo
	loaded   []string
	unloaded []string
	loadErr  error
	delErr   error
}

func (s *stubMgr) List() []sdk.PluginInfo { return s.infos }
func (s *stubMgr) Load(id string) error {
	if s.loadErr != nil {
		return s.loadErr
	}
	s.loaded = append(s.loaded, id)
	return nil
}
func (s *stubMgr) Unload(id string) error {
	if s.delErr != nil {
		return s.delErr
	}
	s.unloaded = append(s.unloaded, id)
	return nil
}

// stubWs 工作区桩(实现 /workspace 用到的会话切面)。
type stubWs struct {
	sdk.CwdSessions
	cur   string
	newID string
	recs  []sdk.ProjectInfo
	swErr error
	swKey string
}

func (s *stubWs) Current() string { return s.cur }
func (s *stubWs) SwitchProject(key string) (string, error) {
	s.swKey = key
	if s.swErr != nil {
		return "", s.swErr
	}
	return s.newID, nil
}
func (s *stubWs) RecentProjects() []sdk.ProjectInfo { return s.recs }

// providerHome 隔离 GAH_HOME(providerfile/prefs/install 都落这里)。
func providerHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	return home
}

// TestPluginName 插件名 = 登记 id。
func TestPluginName(t *testing.T) {
	if got := (&Plugin{}).Name(); got != "host-internal-commands" {
		t.Fatalf("Name()=%q", got)
	}
}

// TestCommandsExecuteProviderShow /provider show:未装配/非多 provider/空/有 provider(★ + 打码)。
func TestCommandsExecuteProviderShow(t *testing.T) {
	// 未装配 llm:显式报错
	c, cmds := buildEnv(t)
	startCmds(t, c)
	if _, err := run(t, cmds, "provider", "show"); err == nil || !strings.Contains(err.Error(), "ctx.llm 未装配") {
		t.Fatalf("未装配 llm 应显式报错: %v", err)
	}

	// llm 未实现 MultiProviderService:显式拒绝(不静默回退)
	c2, cmds2 := buildEnv(t)
	if err := c2.Provide("ctx.llm", sdk.LLMService(&stubLLMPlain{})); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c2)
	if _, err := run(t, cmds2, "provider", "show"); err == nil || !strings.Contains(err.Error(), "MultiProviderService") {
		t.Fatalf("非多 provider 应显式拒绝: %v", err)
	}

	// 多 provider:空列表 → 提示配置;有 provider → ★/打码/模型
	providerHome(t)
	c3, cmds3 := buildEnv(t)
	ms := &stubMultiLLM{}
	if err := c3.Provide("ctx.llm", sdk.LLMService(ms)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c3)
	out, err := run(t, cmds3, "provider", "show")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/provider add") {
		t.Fatalf("空 provider 应提示配置: %q", out)
	}

	if _, err := run(t, cmds3, "provider", "add", "https://api.siliconflow.cn/v1", "sk-1234567890abcdef", "deepseek-ai/DeepSeek-V3"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, cmds3, "provider", "add", "https://api.deepseek.com/v1", "sk-abcdefghijklmnop"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, cmds3, "provider", "show")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "★ siliconflow") {
		t.Fatalf("活跃 provider 应标 ★: %q", out)
	}
	if !strings.Contains(out, "sk-1…cdef") {
		t.Fatalf("凭据应打码: %q", out)
	}
	if strings.Contains(out, "sk-1234567890abcdef") {
		t.Fatalf("凭据不得原文展示: %q", out)
	}
	if !strings.Contains(out, "模型: deepseek-ai/DeepSeek-V3") || !strings.Contains(out, "未设置") {
		t.Fatalf("应展示模型(缺省标未设置): %q", out)
	}
}

// TestCommandsExecuteProviderAdd /provider add:缺参/失败上抛/首个自动激活/后续保持活跃。
func TestCommandsExecuteProviderAdd(t *testing.T) {
	providerHome(t)
	c, cmds := buildEnv(t)
	ms := &stubMultiLLM{}
	if err := c.Provide("ctx.llm", sdk.LLMService(ms)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)

	if _, err := run(t, cmds, "provider", "add", "https://api.siliconflow.cn/v1"); err == nil ||
		!strings.Contains(err.Error(), "baseUrl") {
		t.Fatalf("缺参应给用法: %v", err)
	}

	out, err := run(t, cmds, "provider", "add", "https://api.siliconflow.cn/v1", "sk-aaaabbbb", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已添加并激活 provider siliconflow") {
		t.Fatalf("首个 provider 应自动激活: %q", out)
	}
	if f, _ := providerfile.LoadFile(); f.Active != "siliconflow" || len(f.Providers) != 1 {
		t.Fatalf("应落盘并激活: %+v", f)
	}

	out, err = run(t, cmds, "provider", "add", "https://api.deepseek.com/v1", "sk-ccccdddd")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "当前活跃保持 siliconflow") || !strings.Contains(out, "/provider use deepseek") {
		t.Fatalf("新增不应夺走活跃,并应提示切换: %q", out)
	}

	ms.addErr = fmt.Errorf("端点不可达")
	if _, err := run(t, cmds, "provider", "add", "https://api.x.cn/v1", "sk-eeee"); err == nil ||
		!strings.Contains(err.Error(), "端点不可达") {
		t.Fatalf("AddProvider 失败应上抛: %v", err)
	}
}

// TestCommandsExecuteProviderUse /provider use:缺参/切换成功(回报端点)/切换失败/未知名回落 ?。
func TestCommandsExecuteProviderUse(t *testing.T) {
	providerHome(t)
	c, cmds := buildEnv(t)
	ms := &stubMultiLLM{}
	if err := c.Provide("ctx.llm", sdk.LLMService(ms)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	if _, err := run(t, cmds, "provider", "add", "https://api.siliconflow.cn/v1", "sk-aaaabbbb"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, cmds, "provider", "use"); err == nil || !strings.Contains(err.Error(), "use <name>") {
		t.Fatalf("缺参应给用法: %v", err)
	}
	out, err := run(t, cmds, "provider", "use", "siliconflow")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已切换 → siliconflow") || !strings.Contains(out, "https://api.siliconflow.cn/v1") {
		t.Fatalf("切换应回报端点: %q", out)
	}
	if ms.active != "siliconflow" {
		t.Fatalf("运行期未切换: %q", ms.active)
	}
	// 内存有、文件无的名字:端点回落 ?
	ms.mu.Lock()
	ms.providers = append(ms.providers, sdk.ProviderProfile{Name: "ghost"})
	ms.mu.Unlock()
	out, err = run(t, cmds, "provider", "use", "ghost")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "端点: ?") {
		t.Fatalf("未知端点应回落 ?: %q", out)
	}
	// 运行期切换失败:显式上抛
	ms.useErr = fmt.Errorf("适配器未实现 Configure")
	if _, err := run(t, cmds, "provider", "use", "siliconflow"); err == nil ||
		!strings.Contains(err.Error(), "Configure") {
		t.Fatalf("切换失败应上抛: %v", err)
	}
}

// TestCommandsExecuteProviderSet /provider set:无活跃 → 新建并激活;有活跃 → 局部更新 + 立即生效。
func TestCommandsExecuteProviderSet(t *testing.T) {
	providerHome(t)
	c, cmds := buildEnv(t)
	ms := &stubMultiLLM{}
	if err := c.Provide("ctx.llm", sdk.LLMService(ms)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)

	if _, err := run(t, cmds, "provider", "set", "https://api.x.cn/v1"); err == nil ||
		!strings.Contains(err.Error(), "baseUrl") {
		t.Fatalf("缺参应给用法: %v", err)
	}
	// 无活跃 provider:set = 新建并激活(switchAddAsSet)
	out, err := run(t, cmds, "provider", "set", "https://api.siliconflow.cn/v1", "sk-aaaabbbb", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已更新活跃 provider siliconflow") {
		t.Fatalf("set 应新建并激活: %q", out)
	}
	if f, _ := providerfile.LoadFile(); f.Active != "siliconflow" || f.Providers[0].Model != "m1" {
		t.Fatalf("应落盘: %+v", f)
	}
	// 有活跃:局部更新端点/凭据,未给的字段保留(模型仍 m1)
	out, err = run(t, cmds, "provider", "set", "https://api.siliconflow.cn/v2", "sk-ccccdddd")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "https://api.siliconflow.cn/v2") {
		t.Fatalf("应回报新端点: %q", out)
	}
	f, _ := providerfile.LoadFile()
	if f.Providers[0].BaseURL != "https://api.siliconflow.cn/v2" || f.Providers[0].Model != "m1" || f.Providers[0].APIKey != "sk-ccccdddd" {
		t.Fatalf("局部更新语义不符(空字段应保留旧值): %+v", f.Providers[0])
	}
	if ms.active != "siliconflow" {
		t.Fatalf("set 应令运行期重新指向活跃 provider: %q", ms.active)
	}
}

// TestCommandsExecuteProviderUnsetClear /provider unset|clear:逐项删除回退 + 全清复位。
func TestCommandsExecuteProviderUnsetClear(t *testing.T) {
	providerHome(t)
	c, cmds := buildEnv(t)
	ms := &stubMultiLLM{}
	if err := c.Provide("ctx.llm", sdk.LLMService(ms)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	if _, err := run(t, cmds, "provider", "add", "https://api.siliconflow.cn/v1", "sk-aaaabbbb", "m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, cmds, "provider", "unset"); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("缺参应给字段提示: %v", err)
	}
	if _, err := run(t, cmds, "provider", "unset", "bogus"); err == nil || !strings.Contains(err.Error(), "未知字段") {
		t.Fatalf("未知字段应报错: %v", err)
	}
	out, err := run(t, cmds, "provider", "unset", "api_key")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已删除 api_key") || len(ms.unset) != 1 || ms.unset[0] != "api_key" {
		t.Fatalf("应同时回退持久化与运行期: out=%q unset=%v", out, ms.unset)
	}
	if f, _ := providerfile.LoadFile(); f.Providers[0].APIKey != "" || f.Providers[0].BaseURL == "" {
		t.Fatalf("应只删该字段: %+v", f.Providers[0])
	}
	// 末项删除 → provider 全空 → 被移除并重置活跃
	out, err = run(t, cmds, "provider", "unset", "model")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已删除 model") {
		t.Fatalf("unset model: %q", out)
	}
	// clear:删文件 + 运行时复位
	out, err = run(t, cmds, "provider", "clear")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已清除全部 provider") || !ms.reset {
		t.Fatalf("clear 应复位运行期: out=%q reset=%v", out, ms.reset)
	}
	if _, err := os.Stat(providerfile.Path()); err == nil {
		t.Fatal("clear 应删除 provider.yaml")
	}
}

// TestCommandsExecuteProviderBadSubcommand 无参/未知子命令走用法分支(不静默)。
func TestCommandsExecuteProviderBadSubcommand(t *testing.T) {
	providerHome(t)
	c, cmds := buildEnv(t)
	startCmds(t, c)
	for _, args := range [][]string{{}, {"bogus"}} {
		if _, err := run(t, cmds, "provider", args...); err == nil ||
			!strings.Contains(err.Error(), "show|add|use|set|unset|clear") {
			t.Fatalf("应给用法提示(%v): %v", args, err)
		}
	}
}

// TestProviderSelectorOptions /provider 选择器:一级子命令枚举、二级 use 枚举/ unset 字段/ add|set 自由参数。
func TestProviderSelectorOptions(t *testing.T) {
	providerHome(t)
	c, cmds := buildEnv(t)
	ms := &stubMultiLLM{}
	if err := c.Provide("ctx.llm", sdk.LLMService(ms)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	spec, ok := cmds.Get("provider")
	if !ok {
		t.Fatal("provider 未注册")
	}
	if len(spec.Args) != 2 {
		t.Fatalf("/provider 应有 2 级参数: %+v", spec.Args)
	}
	vals := map[string]bool{}
	for _, o := range spec.Args[0].Options(nil) {
		vals[o.Value] = true
	}
	for _, want := range []string{"show", "add", "use", "set", "unset", "clear"} {
		if !vals[want] {
			t.Fatalf("一级选项缺 %s: %v", want, vals)
		}
	}
	if got := spec.Args[1].Options([]string{"provider"}); got != nil {
		t.Fatalf("缺子命令时二级应为空: %+v", got)
	}
	if got := spec.Args[1].Options([]string{"provider", "show"}); got != nil {
		t.Fatalf("show 无二级枚举: %+v", got)
	}
	if _, err := run(t, cmds, "provider", "add", "https://api.siliconflow.cn/v1", "sk-aaaabbbb"); err != nil {
		t.Fatal(err)
	}
	use := spec.Args[1].Options([]string{"provider", "use"})
	if len(use) != 1 || use[0].Value != "siliconflow" || use[0].Desc != "https://api.siliconflow.cn/v1" {
		t.Fatalf("use 应枚举现有 provider: %+v", use)
	}
	unset := spec.Args[1].Options([]string{"provider", "unset"})
	if len(unset) != 3 || unset[0].Value != "base_url" {
		t.Fatalf("unset 应枚举三个字段: %+v", unset)
	}
	fa := spec.Args[1].FreeArgs([]string{"provider", "add"})
	if len(fa) != 3 || fa[2] != "model?" {
		t.Fatalf("add 应提示 baseUrl/apiKey/model?: %v", fa)
	}
	if fa := spec.Args[1].FreeArgs([]string{"provider", "set"}); len(fa) != 3 {
		t.Fatalf("set 应提示三步输入: %v", fa)
	}
	if fa := spec.Args[1].FreeArgs([]string{"provider", "clear"}); fa != nil {
		t.Fatalf("clear 无自由参数: %v", fa)
	}
	if fa := spec.Args[1].FreeArgs([]string{"provider"}); fa != nil {
		t.Fatalf("缺子命令无自由参数: %v", fa)
	}
	// providerLevel2 读文件失败分支:清空文件后仍不 panic(空列表)
	if err := os.Remove(providerfile.Path()); err != nil {
		t.Fatal(err)
	}
	if got := spec.Args[1].Options([]string{"provider", "use"}); len(got) != 0 {
		t.Fatalf("无 provider 时 use 枚举为空: %+v", got)
	}
}

// TestCommandsExecuteModel /model:缺参/带 provider 前缀自动切/纯模型名/持久化同步/未装配 llm。
func TestCommandsExecuteModel(t *testing.T) {
	home := providerHome(t)
	c, cmds := buildEnv(t)
	ms := &stubMultiLLM{}
	if err := c.Provide("ctx.llm", sdk.LLMService(ms)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	if _, err := run(t, cmds, "model"); err == nil || !strings.Contains(err.Error(), "/model <名称>") {
		t.Fatalf("缺参应给用法: %v", err)
	}
	if _, err := run(t, cmds, "provider", "add", "https://api.siliconflow.cn/v1", "sk-aaaabbbb"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, cmds, "model", "siliconflow|deepseek-ai/DeepSeek-V3"); err != nil {
		t.Fatal(err)
	}
	if ms.curModel != "deepseek-ai/DeepSeek-V3" || ms.active != "siliconflow" {
		t.Fatalf("带前缀应解析并自动切 provider: model=%q active=%q", ms.curModel, ms.active)
	}
	if f, _ := providerfile.LoadFile(); f.Providers[0].Model != "deepseek-ai/DeepSeek-V3" {
		t.Fatalf("应同步持久化活跃 provider 的模型: %+v", f.Providers[0])
	}
	// 纯模型名:只切模型
	if _, err := run(t, cmds, "model", "deepseek-chat"); err != nil {
		t.Fatal(err)
	}
	if ms.curModel != "deepseek-chat" {
		t.Fatalf("纯模型名应生效: %q", ms.curModel)
	}
	// 持久化失败不致命:返回提示而非错误(provider.yaml 目录被占为文件)
	if err := os.RemoveAll(filepath.Join(home, "config")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, cmds, "model", "m2")
	if err != nil {
		t.Fatalf("持久化失败不应中断切换: %v", err)
	}
	if !strings.Contains(out, "持久化同步失败") {
		t.Fatalf("应提示持久化失败: %q", out)
	}

	// llm 未装配:显式报错
	c2, cmds2 := buildEnv(t)
	startCmds(t, c2)
	if _, err := run(t, cmds2, "model", "m"); err == nil || !strings.Contains(err.Error(), "ctx.llm 未装配") {
		t.Fatalf("未装配 llm 应显式报错: %v", err)
	}
}

// TestModelSelectorOptions /model 枚举:多 provider 聚合(跳失败/空端点)、单 provider 回退、无 llm 空。
func TestModelSelectorOptions(t *testing.T) {
	providerHome(t)
	c, cmds := buildEnv(t)
	ms := &stubMultiLLM{
		models: []sdk.ProviderModelList{
			{Name: "siliconflow", Models: []sdk.ModelInfo{
				{ID: "deepseek-ai/DeepSeek-V3", OwnedBy: "deepseek-ai"},
				{ID: "Qwen/Qwen2.5-72B", OwnedBy: "other"},
			}},
			{Name: "broken", Err: fmt.Errorf("拉取失败")},
			{Name: "empty"},
		},
		base: "https://api.siliconflow.cn/v1", infoOK: true,
	}
	if err := c.Provide("ctx.llm", sdk.LLMService(ms)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	spec, _ := cmds.Get("model")
	opts := spec.Args[0].Options(nil)
	if len(opts) != 2 {
		t.Fatalf("应只列成功且有模型的端点: %+v", opts)
	}
	if opts[0].Value != "siliconflow|deepseek-ai/DeepSeek-V3" {
		t.Fatalf("选项值应带来源前缀: %+v", opts[0])
	}
	if !strings.Contains(opts[0].Desc, "(来源 siliconflow)") {
		t.Fatalf("归属与来源一致时不重复标注: %+v", opts[0])
	}
	if !strings.Contains(opts[1].Desc, "归属 other") {
		t.Fatalf("归属与来源不一致应标注两者: %+v", opts[1])
	}

	// 单 provider 回退:llm 仅实现 ListModels + ProviderInfo(未实现 MultiProviderService)
	c2, cmds2 := buildEnv(t)
	single := &stubSingleLLM{list: []sdk.ModelInfo{{ID: "gpt-4o", OwnedBy: "openai"}}, base: "https://api.deepseek.com/v1", infoOK: true}
	if err := c2.Provide("ctx.llm", sdk.LLMService(single)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c2)
	singleSpec, _ := cmds2.Get("model")
	singleOpts := singleSpec.Args[0].Options(nil)
	if len(singleOpts) != 1 || singleOpts[0].Value != "gpt-4o" {
		t.Fatalf("单 provider 回退应列端点模型: %+v", singleOpts)
	}
	if !strings.Contains(singleOpts[0].Desc, "来源 deepseek/归属 openai") {
		t.Fatalf("归属与来源不同应同时标注: %+v", singleOpts[0])
	}
	// 无端点信息:来源标注 ?
	single.infoOK = false
	if got := singleSpec.Args[0].Options(nil); len(got) != 1 || !strings.Contains(got[0].Desc, "来源 ?") {
		t.Fatalf("无端点信息时来源应为 ?: %+v", got)
	}
	// ListModels 失败 → 空枚举(回退手动输入)
	single.listErr = fmt.Errorf("端点不可达")
	if got := singleSpec.Args[0].Options(nil); got != nil {
		t.Fatalf("拉取失败应回退手动输入: %+v", got)
	}

	// 无 llm:空枚举不 panic
	c3, cmds3 := buildEnv(t)
	startCmds(t, c3)
	spec3, _ := cmds3.Get("model")
	if got := spec3.Args[0].Options(nil); got != nil {
		t.Fatalf("无 llm 应空枚举: %+v", got)
	}
	if got := (&Host{c: c3}).providerShort(); got != "?" {
		t.Fatalf("无 llm 时来源应为 ?: %q", got)
	}
}

// TestCommandsExecutePlugins /plugins:list/on/off/default 的持久化与 profile 注入。
func TestCommandsExecutePlugins(t *testing.T) {
	home := providerHome(t)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(home, "config", "profile-test.yaml")
	if err := os.WriteFile(profile, []byte("name: test\nbundles: [base]\npatches: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, cmds := buildEnv(t)
	mgr := &stubMgr{infos: []sdk.PluginInfo{
		{ID: "tool-shell", Type: "tool", State: "loaded"},
		{ID: "host-jobs", Type: "host", State: "configured"},
	}}
	if err := c.Provide("ctx.pluginManager", sdk.PluginManager(mgr)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)

	// 未装配 ctx.pluginManager:显式报错
	c2, cmds2 := buildEnv(t)
	startCmds(t, c2)
	if _, err := run(t, cmds2, "plugins"); err == nil || !strings.Contains(err.Error(), "ctx.pluginManager 未装配") {
		t.Fatalf("未装配应显式报错: %v", err)
	}

	out, err := run(t, cmds, "plugins")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tool-shell [tool] loaded") || !strings.Contains(out, "host-jobs [host] configured") {
		t.Fatalf("list 应列出 id/类别/状态: %q", out)
	}
	if _, err := run(t, cmds, "plugins", "on"); err == nil || !strings.Contains(err.Error(), "on <id>") {
		t.Fatalf("on 缺参应给用法: %v", err)
	}
	out, err = run(t, cmds, "plugins", "on", "host-jobs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已加载并持久启用 host-jobs") || len(mgr.loaded) != 1 {
		t.Fatalf("on 应加载: out=%q loaded=%v", out, mgr.loaded)
	}
	// 持久化:patch-runtime.yaml 记 enabled=true,且 profile 注入引用
	patch := install.RuntimePatch(home)
	if got := install.ReadEnablements(patch); got["host-jobs"] != true {
		t.Fatalf("应写入持久开关: %+v", got)
	}
	pb, _ := os.ReadFile(profile)
	if !strings.Contains(string(pb), "patch-runtime.yaml") {
		t.Fatalf("profile 应注入 patch 引用: %s", pb)
	}
	// list 带持久后缀
	out, err = run(t, cmds, "plugins", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "host-jobs [host] configured (持久开)") {
		t.Fatalf("应标注持久开: %q", out)
	}
	// off:卸载 + 持久关闭
	out, err = run(t, cmds, "plugins", "off", "host-jobs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已卸载并持久关闭 host-jobs") || len(mgr.unloaded) != 1 {
		t.Fatalf("off 应卸载: out=%q unloaded=%v", out, mgr.unloaded)
	}
	if got := install.ReadEnablements(patch); got["host-jobs"] != false {
		t.Fatalf("应写持久关闭: %+v", got)
	}
	out, _ = run(t, cmds, "plugins", "list")
	if !strings.Contains(out, "(持久关)") {
		t.Fatalf("应标注持久关: %q", out)
	}
	// default:清除持久覆盖
	out, err = run(t, cmds, "plugins", "default", "host-jobs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已清除持久覆盖 host-jobs") {
		t.Fatalf("default 应清除: %q", out)
	}
	if got := install.ReadEnablements(patch); len(got) != 0 {
		t.Fatalf("持久覆盖应清空: %+v", got)
	}
	// load/unload 别名 + 失败上抛 + 缺参 + 未知子命令
	if _, err := run(t, cmds, "plugins", "off"); err == nil || !strings.Contains(err.Error(), "off <id>") {
		t.Fatalf("off 缺参应给用法: %v", err)
	}
	if _, err := run(t, cmds, "plugins", "bogus"); err == nil || !strings.Contains(err.Error(), "list|on|off|default") {
		t.Fatalf("未知子命令应给用法: %v", err)
	}
	mgr.loadErr = fmt.Errorf("依赖缺失")
	if _, err := run(t, cmds, "plugins", "load", "tool-shell"); err == nil || !strings.Contains(err.Error(), "依赖缺失") {
		t.Fatalf("Load 失败应上抛: %v", err)
	}
	mgr.delErr = fmt.Errorf("被依赖占用")
	if _, err := run(t, cmds, "plugins", "unload", "tool-shell"); err == nil || !strings.Contains(err.Error(), "被依赖占用") {
		t.Fatalf("Unload 失败应上抛: %v", err)
	}
	if _, err := run(t, cmds, "plugins", "default"); err == nil || !strings.Contains(err.Error(), "default <id>") {
		t.Fatalf("default 缺参应给用法: %v", err)
	}
}

// TestPluginsSelectorOptions /plugins 选择器:一级子命令;二级枚举插件 id(未装配时为空)。
func TestPluginsSelectorOptions(t *testing.T) {
	providerHome(t)
	c, cmds := buildEnv(t)
	mgr := &stubMgr{infos: []sdk.PluginInfo{{ID: "tool-shell", Type: "tool"}}}
	if err := c.Provide("ctx.pluginManager", sdk.PluginManager(mgr)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	spec, _ := cmds.Get("plugins")
	vals := map[string]bool{}
	for _, o := range spec.Args[0].Options(nil) {
		vals[o.Value] = true
	}
	if !vals["list"] || !vals["on"] || !vals["off"] || !vals["default"] {
		t.Fatalf("一级选项不全: %v", vals)
	}
	if got := spec.Args[1].Options([]string{"plugins", "list"}); got != nil {
		t.Fatalf("list 无二级枚举: %+v", got)
	}
	if got := spec.Args[1].Options([]string{"plugins"}); got != nil {
		t.Fatalf("缺子命令无二级枚举: %+v", got)
	}
	opts := spec.Args[1].Options([]string{"plugins", "on"})
	if len(opts) != 1 || opts[0].Value != "tool-shell" || opts[0].Desc != "tool" {
		t.Fatalf("二级应枚举插件 id: %+v", opts)
	}
	// 未装配 pluginManager:二级空(不 panic)
	c2, cmds2 := buildEnv(t)
	startCmds(t, c2)
	spec2, _ := cmds2.Get("plugins")
	if got := spec2.Args[1].Options([]string{"plugins", "on"}); got != nil {
		t.Fatalf("未装配应空枚举: %+v", got)
	}
}

// TestCommandsExecuteWorkspace /workspace:路径解析(相对/~)/不存在/非目录/成功切换/未装配会话。
func TestCommandsExecuteWorkspace(t *testing.T) {
	providerHome(t)
	base := t.TempDir()
	target := filepath.Join(base, "ws")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)

	c, cmds := buildEnv(t)
	ws := &stubWs{cur: "proj-key", newID: "s-new"}
	if err := c.Provide("ctx.cwdSessions", sdk.CwdSessions(ws)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)

	if _, err := run(t, cmds, "workspace"); err == nil || !strings.Contains(err.Error(), "/workspace [目录]") {
		t.Fatalf("无参应给用法: %v", err)
	}
	if _, err := run(t, cmds, "workspace", "totally-missing-dir"); err == nil ||
		!strings.Contains(err.Error(), "目录不存在或不可访问") {
		t.Fatalf("不存在目录应显式报错: %v", err)
	}
	if _, err := run(t, cmds, "workspace", file); err == nil || !strings.Contains(err.Error(), "非目录") {
		t.Fatalf("文件路径应报非目录: %v", err)
	}
	// 相对路径:解析为绝对路径后 chdir + 切项目(新会话)
	out, err := run(t, cmds, "workspace", "ws")
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	resolved := target
	if link, err := filepath.EvalSymlinks(target); err == nil {
		resolved = link // macOS 临时目录经 /var → /private/var 符号链接
	}
	if wd != resolved {
		t.Fatalf("应 chdir 到目标: got %q want %q", wd, resolved)
	}
	if !strings.Contains(out, "已切换工作区 → "+target) || !strings.Contains(out, "新会话 s-new") {
		t.Fatalf("应回报工作区与新会话: %q", out)
	}
	if ws.swKey == "" {
		t.Fatal("应经 SwitchProject 切会话")
	}
	// 哨兵前缀:选择器选“新目录”后按路径解析
	out, err = run(t, cmds, "workspace", workspaceNewSentinel+base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已切换工作区 → "+base) {
		t.Fatalf("哨兵应被剥离: %q", out)
	}
	// ~ 展开
	uh := t.TempDir()
	t.Setenv("HOME", uh)
	out, err = run(t, cmds, "workspace", "~")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已切换工作区 → "+uh) {
		t.Fatalf("~ 应展开为用户主目录: %q", out)
	}

	// 会话切换失败:显式报错
	ws.swErr = fmt.Errorf("会话文件损坏")
	if _, err := run(t, cmds, "workspace", target); err == nil || !strings.Contains(err.Error(), "会话切换失败") {
		t.Fatalf("SwitchProject 失败应上抛: %v", err)
	}
	// 未装配 cwdSessions:仅 chdir 提示
	c2, cmds2 := buildEnv(t)
	startCmds(t, c2)
	out, err = run(t, cmds2, "workspace", target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "未装配 cwdSessions,仅 chdir") {
		t.Fatalf("未装配应仅 chdir: %q", out)
	}
}

// TestWorkspaceSelectorOptions /workspace 选择器:最近工作区 + 新建哨兵;二级仅哨兵要自由输入。
func TestWorkspaceSelectorOptions(t *testing.T) {
	providerHome(t)
	now := int64(1700000000)
	c, cmds := buildEnv(t)
	ws := &stubWs{recs: []sdk.ProjectInfo{
		{Key: "k1", Dir: "/tmp/d1", TS: now},
		{Key: "k2", Dir: "/tmp/d2", TS: now - 100},
	}}
	if err := c.Provide("ctx.cwdSessions", sdk.CwdSessions(ws)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	spec, _ := cmds.Get("workspace")
	opts := spec.Args[0].Options(nil)
	if len(opts) != 3 {
		t.Fatalf("应列 2 条最近工作区 + 哨兵: %+v", opts)
	}
	if opts[0].Value != "/tmp/d1" || !strings.Contains(opts[0].Desc, "k1") {
		t.Fatalf("选项应带真实目录与 key: %+v", opts[0])
	}
	if opts[2].Value != workspaceNewSentinel || !strings.Contains(opts[2].Desc, "输入新目录") {
		t.Fatalf("末项应为新建哨兵: %+v", opts[2])
	}
	fa := spec.Args[1].FreeArgs([]string{"workspace", workspaceNewSentinel})
	if len(fa) != 1 || fa[0] != "目录路径" {
		t.Fatalf("哨兵二级应要自由输入: %v", fa)
	}
	if fa := spec.Args[1].FreeArgs([]string{"workspace", "/tmp/d1"}); fa != nil {
		t.Fatalf("选历史目录无二级输入: %v", fa)
	}
	// 无历史记录:一级空(直接走自由输入)
	ws.recs = nil
	if got := spec.Args[0].Options(nil); got != nil {
		t.Fatalf("无历史应空枚举: %+v", got)
	}
	// 未装配 cwdSessions:空枚举
	c2, cmds2 := buildEnv(t)
	startCmds(t, c2)
	spec2, _ := cmds2.Get("workspace")
	if got := spec2.Args[0].Options(nil); got != nil {
		t.Fatalf("未装配应空枚举: %+v", got)
	}
}

// stubSingleLLM 单 provider LLM 桩:实现 ListModels + ProviderInfo(不实现 MultiProviderService,
// 用于 /model 枚举的旧单端点回退路径)。
type stubSingleLLM struct {
	sdk.LLMService
	list    []sdk.ModelInfo
	listErr error
	base    string
	infoOK  bool
	model   string
}

func (s *stubSingleLLM) ListModels() ([]sdk.ModelInfo, error) { return s.list, s.listErr }

func (s *stubSingleLLM) ProviderInfo() (string, string, bool) { return s.base, "sk-x", s.infoOK }

func (s *stubSingleLLM) SetModel(m string) { s.model = m }

// stubCwdSessions 会话列表桩(/session switch 与 /workspace 二级枚举用)。
type stubCwdSessions struct {
	sdk.CwdSessions
	sessions []sdk.SessionInfo
	recs     []sdk.ProjectInfo
}

func (s *stubCwdSessions) Sessions() []sdk.SessionInfo       { return s.sessions }
func (s *stubCwdSessions) RecentProjects() []sdk.ProjectInfo { return s.recs }

// TestCommandsExecuteApprovalBadMode /approval 未知档位 = 显式报错(不静默保持旧档)。
func TestCommandsExecuteApprovalBadMode(t *testing.T) {
	c, cmds := buildEnv(t)
	t.Setenv("GAH_HOME", t.TempDir())
	st := &stubApproval{}
	if err := c.Provide("ctx.approval", st); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	if _, err := run(t, cmds, "approval", "bogus"); err == nil ||
		!strings.Contains(err.Error(), "open|smart|strict") {
		t.Fatalf("未知档位应给用法: %v", err)
	}
	if st.mode != "" {
		t.Fatalf("非法档位不应改动服务: %q", st.mode)
	}
}

// TestSessionSwitchSelectorOptions /session switch 二级:枚举会话(主会话用 main 标识)+
// 非 switch 子命令/未装配时不枚举。
func TestSessionSwitchSelectorOptions(t *testing.T) {
	providerHome(t)
	c, cmds := buildEnv(t)
	cs := &stubCwdSessions{sessions: []sdk.SessionInfo{
		{ID: "", Name: "主", MTime: 1700000000, Frames: 3},
		{ID: "s2", MTime: 1700000000, Frames: -1},
		{ID: "s3", Name: "命名会话", MTime: 0, Frames: 0},
	}}
	if err := c.Provide("ctx.cwdSessions", sdk.CwdSessions(cs)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	spec, _ := cmds.Get("session")
	opts := spec.Args[1].Options([]string{"session", "switch"})
	if len(opts) != 3 {
		t.Fatalf("应枚举全部会话: %+v", opts)
	}
	if opts[0].Value != "main" || !strings.Contains(opts[0].Desc, "主") {
		t.Fatalf("主会话应用 main 标识: %+v", opts[0])
	}
	if strings.Contains(opts[0].Desc, "条") {
		t.Fatalf("主会话不标条数: %+v", opts[0])
	}
	if opts[1].Value != "s2" || !strings.Contains(opts[1].Desc, "会话 s2") || strings.Contains(opts[1].Desc, "条") {
		t.Fatalf("无名称回退会话 id;-1 条数不展示: %+v", opts[1])
	}
	if !strings.Contains(opts[2].Desc, "命名会话") || !strings.Contains(opts[2].Desc, "0 条") {
		t.Fatalf("有名称用名称并标条数: %+v", opts[2])
	}
	if got := spec.Args[1].Options([]string{"session", "list"}); got != nil {
		t.Fatalf("非 switch 不枚举: %+v", got)
	}
	if got := spec.Args[1].Options([]string{"session"}); got != nil {
		t.Fatalf("缺子命令不枚举: %+v", got)
	}
	// 未装配 cwdSessions:空枚举不 panic
	c2, cmds2 := buildEnv(t)
	startCmds(t, c2)
	spec2, _ := cmds2.Get("session")
	if got := spec2.Args[1].Options([]string{"session", "switch"}); got != nil {
		t.Fatalf("未装配应空枚举: %+v", got)
	}
}

// TestProviderSetNameMismatch /provider set 时运行期服务给出的名字与派生短名不符(名称归一化差异):
// 必须显式上抛,不静默当作已激活。
func TestProviderSetNameMismatch(t *testing.T) {
	providerHome(t)
	c, cmds := buildEnv(t)
	ms := &stubMultiLLM{addNameOverride: "custom-name"}
	if err := c.Provide("ctx.llm", sdk.LLMService(ms)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	if _, err := run(t, cmds, "provider", "set", "https://api.siliconflow.cn/v1", "sk-aaaabbbb"); err == nil {
		t.Fatal("名字不符且切换失败应显式上抛")
	}
}

// TestAllSpecSelectorLevelsEnumerate 全命令选择器级联枚举不 panic 且自洽:
// 每级拿到的 Option 值非空、同级不重复(重复值会让选择器无法区分),FreeArgs 名非空;
// 一级枚举的命令必须给出候选项(否则交互式 UI 直接卡死)。
// 服务集齐时逐条命令逐级展开(覆盖 specs 内各动态枚举闭包)。
func TestAllSpecSelectorLevelsEnumerate(t *testing.T) {
	providerHome(t)
	c, cmds := buildEnv(t)
	ms := &stubMultiLLM{
		models: []sdk.ProviderModelList{{Name: "siliconflow", Models: []sdk.ModelInfo{{ID: "m1"}, {ID: "m2"}}}},
		list:   []sdk.ModelInfo{{ID: "m3"}},
	}
	if err := c.Provide("ctx.llm", sdk.LLMService(ms)); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.pluginManager", sdk.PluginManager(&stubMgr{infos: []sdk.PluginInfo{
		{ID: "tool-shell", Type: "tool"}, {ID: "host-jobs", Type: "host"},
	}})); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.cwdSessions", sdk.CwdSessions(&stubCwdSessions{sessions: []sdk.SessionInfo{
		{ID: "", Name: "主"}, {ID: "s2"},
	}})); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.sessions", sdk.SessionLog(&stubLog{})); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.sandbox", sdk.Sandbox(&stubSandbox{})); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.approval", &stubApproval{}); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	if _, err := run(t, cmds, "provider", "add", "https://api.siliconflow.cn/v1", "sk-aaaabbbb"); err != nil {
		t.Fatal(err)
	}

	enumerated := map[string]bool{"thinking": true, "model": true, "provider": true, "sandbox": true,
		"approval": true, "plugins": true, "settings": true, "session": true}
	seen := map[string]bool{}
	for _, spec := range cmds.List() {
		picked := []string{spec.Name}
		for lvl := range spec.Args {
			// 级别只需二选一(ArgLevel 文档:一级只有一个生效),故两处都要判空再调
			var opts []sdk.Option
			if spec.Args[lvl].Options != nil {
				opts = spec.Args[lvl].Options(picked)
			}
			var free []string
			if spec.Args[lvl].FreeArgs != nil {
				free = spec.Args[lvl].FreeArgs(picked)
			}
			vals := map[string]bool{}
			for _, o := range opts {
				if o.Value == "" {
					t.Fatalf("%s 第 %d 级出现空值选项(选择器无法使用): %+v", spec.Name, lvl+1, o)
				}
				if vals[o.Value] {
					t.Fatalf("%s 第 %d 级选项值重复: %q", spec.Name, lvl+1, o.Value)
				}
				vals[o.Value] = true
			}
			for _, fa := range free {
				if fa == "" {
					t.Fatalf("%s 第 %d 级自由参数名为空", spec.Name, lvl+1)
				}
			}
			if lvl == 0 && enumerated[spec.Name] && len(opts) == 0 {
				t.Fatalf("%s 一级应有候选(交互式选择器据此展开)", spec.Name)
			}
			if lvl == 0 {
				seen[spec.Name] = len(opts) > 0
			}
			// 用本级的合法选择继续下一级(枚举级用首项、自由级用占位值)
			switch {
			case len(opts) > 0:
				picked = append(picked, opts[0].Value)
			case len(free) > 0:
				picked = append(picked, "占位值")
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("应有命令参与选择器枚举检查")
	}
}

// stubCwdErr 会话桩:Open 失败(用于 /session switch 失败路径)。
type stubCwdErr struct {
	sdk.CwdSessions
	cur  string
	path string
	list []string
}

func (s *stubCwdErr) Current() string        { return s.cur }
func (s *stubCwdErr) Path() string           { return s.path }
func (s *stubCwdErr) List() []string         { return s.list }
func (s *stubCwdErr) Open(string) error      { return fmt.Errorf("会话文件损坏") }
func (s *stubCwdErr) New() (string, error)   { return "n9", nil }
func (s *stubCwdErr) CurrentSession() string { return "" }
func (s *stubCwdErr) SessionName() string    { return "" }

// TestCommandsMissingServiceAndArgErrors 依赖缺失与参数缺失一律显式报错(不静默降级):
// /thinking、/sandbox、/settings、/session、/compact、/reload、/stop 的未装配与非法参数分支。
func TestCommandsMissingServiceAndArgErrors(t *testing.T) {
	providerHome(t)

	// 未装配 llm:/thinking
	c, cmds := buildEnv(t)
	startCmds(t, c)
	if _, err := run(t, cmds, "thinking", "high"); err == nil || !strings.Contains(err.Error(), "ctx.llm 未装配") {
		t.Fatalf("未装配 llm 应显式报错: %v", err)
	}
	if _, err := run(t, cmds, "thinking"); err == nil || !strings.Contains(err.Error(), "off|low") {
		t.Fatalf("缺参应给用法: %v", err)
	}
	// 未装配审批服务
	if _, err := run(t, cmds, "approval", "open"); err == nil || !strings.Contains(err.Error(), "ctx.approval 未装配") {
		t.Fatalf("未装配审批应显式报错: %v", err)
	}
	if _, err := run(t, cmds, "approval"); err == nil || !strings.Contains(err.Error(), "open|smart|strict") {
		t.Fatalf("缺参应给用法: %v", err)
	}
	// 审批三档可用
	ap := &stubApproval{}
	if err := c.Provide("ctx.approval", ap); err != nil {
		t.Fatal(err)
	}
	for mode, want := range map[string]sdk.ApprovalMode{"open": sdk.ApprovalOpen, "smart": sdk.ApprovalSmart, "strict": sdk.ApprovalStrict} {
		if _, err := run(t, cmds, "approval", mode); err != nil {
			t.Fatal(err)
		}
		if ap.mode != want {
			t.Fatalf("档位 %s 未生效: %s", mode, ap.mode)
		}
	}
	// 未装配会话日志/系统提示/回合控制
	if _, err := run(t, cmds, "settings", "history", "10"); err == nil || !strings.Contains(err.Error(), "ctx.sessions 未装配") {
		t.Fatalf("未装配 sessions 应显式报错: %v", err)
	}
	if _, err := run(t, cmds, "compact"); err == nil || !strings.Contains(err.Error(), "ctx.sessions 未装配") {
		t.Fatalf("未装配 sessions 应显式报错: %v", err)
	}
	if _, err := run(t, cmds, "reload"); err == nil || !strings.Contains(err.Error(), "ctx.systemPrompt 未装配") {
		t.Fatalf("未装配 systemPrompt 应显式报错: %v", err)
	}
	if _, err := run(t, cmds, "stop"); err == nil || !strings.Contains(err.Error(), "ctx.turnControl") {
		t.Fatalf("未装配 turnControl 应显式报错: %v", err)
	}
	// 未装配 cwdSessions:/session
	if _, err := run(t, cmds, "session", "list"); err == nil || !strings.Contains(err.Error(), "ctx.cwdSessions 未装配") {
		t.Fatalf("未装配 cwdSessions 应显式报错: %v", err)
	}
}

// TestCommandsSessionSwitchPaths /session 各子命令:list 空/current/switch 主会话归一/缺参/失败/未知子命令。
func TestCommandsSessionSwitchPaths(t *testing.T) {
	providerHome(t)
	c, cmds := buildEnv(t)
	cs := &stubCwdErr{cur: "proj", path: "/tmp/main.jsonl", list: nil}
	if err := c.Provide("ctx.cwdSessions", sdk.CwdSessions(cs)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "session", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已有会话: (无)") {
		t.Fatalf("空会话列表应标 (无): %q", out)
	}
	out, err = run(t, cmds, "session", "current")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "当前项目: proj") || !strings.Contains(out, "落盘: /tmp/main.jsonl") {
		t.Fatalf("current 应回报项目/会话/落盘: %q", out)
	}
	if _, err := run(t, cmds, "session", "switch"); err == nil || !strings.Contains(err.Error(), "switch <会话 id>") {
		t.Fatalf("switch 缺参应给用法: %v", err)
	}
	if _, err := run(t, cmds, "session", "switch", "s1"); err == nil || !strings.Contains(err.Error(), "切换失败") {
		t.Fatalf("Open 失败应上抛: %v", err)
	}
	if _, err := run(t, cmds, "session", "bogus"); err == nil || !strings.Contains(err.Error(), "switch|new|current") {
		t.Fatalf("未知子命令应给用法: %v", err)
	}
	if _, err := run(t, cmds, "session"); err == nil || !strings.Contains(err.Error(), "list|switch|new|current") {
		t.Fatalf("无参应给用法: %v", err)
	}
	// switch main:主会话 id 归一为空串,使用 Open 成功的桩
	c2, cmds2 := buildEnv(t)
	ok := &stubCwd{cur: "proj", curSession: ""}
	if err := c2.Provide("ctx.cwdSessions", sdk.CwdSessions(ok)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c2)
	out, err = run(t, cmds2, "session", "switch", "main")
	if err != nil {
		t.Fatal(err)
	}
	if ok.opened != "" || !strings.Contains(out, "已切换到会话 主会话") {
		t.Fatalf("main 应归一为空会话 id: opened=%q out=%q", ok.opened, out)
	}
}

// TestProviderMissingServiceErrors /provider 各子命令在 llm 缺失时显式报错(不静默)。
func TestProviderMissingServiceErrors(t *testing.T) {
	providerHome(t)
	c, cmds := buildEnv(t)
	startCmds(t, c)
	cases := [][]string{
		{"use", "x"},                           // switchProvider → multiSvc
		{"set", "https://api.x.cn/v1", "sk-a"}, // switchAddAsSet → multiSvc
		{"clear"},                              // providerfile 清理后运行时复位需 llm
		{"show"},                               // multiSvc
	}
	for _, args := range cases {
		if _, err := run(t, cmds, "provider", args...); err == nil || !strings.Contains(err.Error(), "ctx.llm 未装配") {
			t.Fatalf("%v 未装配 llm 应显式报错: %v", args, err)
		}
	}
	// unset:持久化项存在但运行时服务缺失
	if err := providerfile.Add(providerfile.Provider{Name: "siliconflow", BaseURL: "https://api.siliconflow.cn/v1", APIKey: "sk-aaaa"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, cmds, "provider", "unset", "api_key"); err == nil ||
		!strings.Contains(err.Error(), "已删除持久化项,但运行时服务缺失") {
		t.Fatalf("unset 缺运行时服务应显式报错: %v", err)
	}
}

// TestProviderUnsetActiveReset /provider unset 使 provider 全空被移除 → 活跃重置并令运行期跟随;
// 重置后切换失败必须显式上抛(不静默留下与运行期不一致的活跃记录)。
func TestProviderUnsetActiveReset(t *testing.T) {
	providerHome(t)
	if err := providerfile.Add(providerfile.Provider{Name: "a", BaseURL: "https://a.cn/v1", APIKey: "k1"}); err != nil {
		t.Fatal(err)
	}
	if err := providerfile.Add(providerfile.Provider{Name: "b", BaseURL: "https://b.cn/v1", APIKey: "k2"}); err != nil {
		t.Fatal(err)
	}
	if got := providerfile.Active(); got != "a" {
		t.Fatalf("前置:应活跃 a,got %q", got)
	}
	c, cmds := buildEnv(t)
	ms := &stubMultiLLM{providers: []sdk.ProviderProfile{
		{Name: "a", BaseURL: "https://a.cn/v1", Active: true}, {Name: "b", BaseURL: "https://b.cn/v1"},
	}}
	if err := c.Provide("ctx.llm", sdk.LLMService(ms)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	if _, err := run(t, cmds, "provider", "unset", "base_url"); err != nil {
		t.Fatal(err)
	}
	if providerfile.Active() != "a" {
		t.Fatalf("仍有字段时不应移出活跃: %q", providerfile.Active())
	}
	out, err := run(t, cmds, "provider", "unset", "api_key")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已删除 api_key") {
		t.Fatalf("unset 应回报: %q", out)
	}
	if got := providerfile.Active(); got != "b" {
		t.Fatalf("活跃应重置为剩余 provider: %q", got)
	}
	if ms.active != "b" {
		t.Fatalf("运行期应跟随新活跃: %q", ms.active)
	}

	// 重建为 a(活跃)/b 两个 provider:重置为 b 且运行时切换失败 → 显式上抛
	if err := providerfile.Clear(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []providerfile.Provider{
		{Name: "a", BaseURL: "https://a.cn/v1", APIKey: "k1"},
		{Name: "b", BaseURL: "https://b.cn/v1", APIKey: "k2"},
	} {
		if err := providerfile.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	ms.useErr = fmt.Errorf("适配器未实现 Configure")
	ms.active = ""
	if _, err := run(t, cmds, "provider", "unset", "base_url"); err != nil {
		t.Fatalf("删除非末项不改活跃,不应触发切换: %v", err)
	}
	if _, err := run(t, cmds, "provider", "unset", "api_key"); err == nil ||
		!strings.Contains(err.Error(), "运行期切换失败") {
		t.Fatalf("重置后切换失败应上抛: %v", err)
	}

	// 活跃未变(删除的是空字段)→ 不重复切换
	ms.useErr = nil
	ms.active = ""
	if err := providerfile.Clear(); err != nil {
		t.Fatal(err)
	}
	if err := providerfile.Add(providerfile.Provider{Name: "solo", BaseURL: "https://s.cn/v1", APIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, cmds, "provider", "unset", "model"); err != nil {
		t.Fatal(err)
	}
	if ms.active != "" {
		t.Fatalf("活跃未变时不应重复切换: %q", ms.active)
	}
}

// TestPluginsPersistFailure /plugins on|off 持久化失败必须显式回报(不静默假装已持久)。
func TestPluginsPersistFailure(t *testing.T) {
	home := providerHome(t)
	// 让持久化写入失败:GAH_HOME/config 被普通文件占据(MkdirAll 报 not a directory)
	if err := os.WriteFile(filepath.Join(home, "config"), []byte("block"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, cmds := buildEnv(t)
	mgr := &stubMgr{infos: []sdk.PluginInfo{{ID: "tool-shell", Type: "tool"}}}
	if err := c.Provide("ctx.pluginManager", sdk.PluginManager(mgr)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	if _, err := run(t, cmds, "plugins", "on", "tool-shell"); err == nil ||
		!strings.Contains(err.Error(), "已加载,但持久化失败") {
		t.Fatalf("on 持久化失败应显式回报: %v", err)
	}
	if _, err := run(t, cmds, "plugins", "off", "tool-shell"); err == nil ||
		!strings.Contains(err.Error(), "已卸载,但持久化失败") {
		t.Fatalf("off 持久化失败应显式回报: %v", err)
	}
}
