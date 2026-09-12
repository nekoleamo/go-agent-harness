// host-system-prompt 装配路径补测:Start 配置解析(global/project/extra)、Provide 注入、
// AddSection 撤销、Assemble 组装顺序与工具 schema、ReloadInstructions 附加指令重建。
// 既有测试都直接构造 Service;这里覆盖插件的真实入口(Start)与配置分支。
package hostsystemprompt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeCtx 最小 Ctx 桩(仅 Provide/Inject 有实体;其余为 no-op)。
type fakeCtx struct {
	mu    sync.Mutex
	svcs  map[string]any
	dupes []string
}

func newFakeCtx() *fakeCtx { return &fakeCtx{svcs: map[string]any{}} }

func (c *fakeCtx) Provide(key string, svc any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.svcs[key]; ok {
		c.dupes = append(c.dupes, key)
		return errors.New("fakeCtx: 重复注册 " + key)
	}
	c.svcs[key] = svc
	return nil
}

func (c *fakeCtx) Inject(key string, out any) error {
	c.mu.Lock()
	svc, ok := c.svcs[key]
	c.mu.Unlock()
	if !ok {
		return errors.New("fakeCtx: 未装配 " + key)
	}
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("fakeCtx: Inject 目标应为非 nil 指针")
	}
	ev := rv.Elem()
	if !reflect.TypeOf(svc).AssignableTo(ev.Type()) {
		return errors.New("fakeCtx: " + key + " 类型不符")
	}
	ev.Set(reflect.ValueOf(svc))
	return nil
}

func (c *fakeCtx) Subscribe(string, sdk.AnyListener) sdk.Disposer { return func() {} }

func (c *fakeCtx) Emit(context.Context, string, any, sdk.DispatchMode) (any, error) { return nil, nil }

func (c *fakeCtx) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// startPrompt 在隔离的 GAH_HOME/cwd 下装配插件,返回注入的服务与撤销函数。
func startPrompt(t *testing.T, data map[string]any) (sdk.SystemPromptService, sdk.Disposer) {
	t.Helper()
	c := newFakeCtx()
	pl := &Plugin{}
	d, err := pl.Start(c, &sdk.Manifest{ID: "host-system-prompt", Data: data})
	if err != nil {
		t.Fatalf("Start 应成功: %v", err)
	}
	var svc sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &svc); err != nil {
		t.Fatalf("ctx.systemPrompt 应可注入: %v", err)
	}
	if svc == nil {
		t.Fatal("注入的 systemPrompt 服务为 nil")
	}
	return svc, d
}

// TestPluginName 插件名 = 登记 id(装配层按名装卸)。
func TestPluginName(t *testing.T) {
	if got := (&Plugin{}).Name(); got != "host-system-prompt" {
		t.Fatalf("Name()=%q", got)
	}
}

// TestStartProvidesServiceAndDisposer Start 注册服务并返回幂等 disposer。
func TestStartProvidesServiceAndDisposer(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	_, d := startPrompt(t, nil)
	if d == nil {
		t.Fatal("Start 应返回 disposer")
	}
	d()
	d() // 幂等:重复调用无害
}

// TestStartLoadsInstructions 默认配置(global+project 开):全局 AGENTS.md 与 cwd 层级指令都注入。
func TestStartLoadsInstructions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("全局注入标记-A1"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("项目注入标记-B2"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(proj)

	svc, _ := startPrompt(t, nil)
	sys := svc.Assemble(nil, nil)[0].Content
	if !strings.Contains(sys, "全局注入标记-A1") {
		t.Fatalf("应注入全局指令:\n%s", sys)
	}
	if !strings.Contains(sys, "项目注入标记-B2") {
		t.Fatalf("应注入项目指令:\n%s", sys)
	}
	if strings.Index(sys, "全局注入标记-A1") > strings.Index(sys, "项目注入标记-B2") {
		t.Fatalf("全局指令应先于项目指令(顺序即覆盖):\n%s", sys)
	}
}

// TestStartConfigDisablesInstructions data.instructions.global/project=false 时对应指令不读不入。
func TestStartConfigDisablesInstructions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("全局注入标记-A1"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("项目注入标记-B2"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(proj)

	svc, _ := startPrompt(t, map[string]any{"instructions": map[string]any{
		"global": false, "project": false,
	}})
	sys := svc.Assemble(nil, nil)[0].Content
	if strings.Contains(sys, "全局注入标记-A1") {
		t.Fatalf("global=false 不应注入全局指令:\n%s", sys)
	}
	if strings.Contains(sys, "项目注入标记-B2") {
		t.Fatalf("project=false 不应注入项目指令:\n%s", sys)
	}
	if !strings.Contains(sys, "规则:") {
		t.Fatalf("固定引导应始终存在:\n%s", sys)
	}
}

// TestStartExtraInstructions data.instructions.extra:存在的文件注入并编号;
// 非字符串条目与缺失文件跳过(不报错、不注入空块)。
func TestStartExtraInstructions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	t.Chdir(t.TempDir())
	extra := filepath.Join(t.TempDir(), "extra.md")
	if err := os.WriteFile(extra, []byte("附加注入标记-C3"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "nope.md")

	svc, _ := startPrompt(t, map[string]any{"instructions": map[string]any{
		"global": false, "project": false,
		"extra": []any{extra, 42, missing},
	}})
	sys := svc.Assemble(nil, nil)[0].Content
	if !strings.Contains(sys, "附加注入标记-C3") {
		t.Fatalf("应注入附加指令:\n%s", sys)
	}
	if !strings.Contains(sys, "附加指令 1:") {
		t.Fatalf("附加指令应编号:\n%s", sys)
	}
	if strings.Contains(sys, "附加指令 2:") {
		t.Fatalf("非字符串/缺失文件不应占编号:\n%s", sys)
	}
}

// TestStartToleratesMalformedData data 结构不符(instructions 非对象/字段类型不符)时按缺省处理。
func TestStartToleratesMalformedData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("全局注入标记-A1"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	for _, data := range []map[string]any{
		{"instructions": "not-a-map"},
		{"instructions": map[string]any{"global": "yes", "project": 1, "extra": "x"}},
	} {
		svc, _ := startPrompt(t, data)
		sys := svc.Assemble(nil, nil)[0].Content
		if !strings.Contains(sys, "全局注入标记-A1") {
			t.Fatalf("data 结构不符时应回落缺省(global 开):\n%s", sys)
		}
	}
}

// TestAddSectionDisposerIdempotent 片段注册随 Disposer 撤销,且只撤自己、重复调用无害。
func TestAddSectionDisposerIdempotent(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	svc, _ := startPrompt(t, nil)

	dA := svc.AddSection(sdk.SystemPromptSection{Name: "段A", Content: func() string { return "内容A" }})
	svc.AddSection(sdk.SystemPromptSection{Name: "段B", Content: func() string { return "内容B" }})
	sys := svc.Assemble(nil, nil)[0].Content
	if !strings.Contains(sys, "段A:\n内容A") || !strings.Contains(sys, "段B:\n内容B") {
		t.Fatalf("片段应注入:\n%s", sys)
	}
	dA()
	dA() // 幂等
	sys = svc.Assemble(nil, nil)[0].Content
	if strings.Contains(sys, "内容A") {
		t.Fatalf("撤销后段A 应消失:\n%s", sys)
	}
	if !strings.Contains(sys, "内容B") {
		t.Fatalf("撤销只应影响自己:\n%s", sys)
	}
}

// TestAssembleOrderToolsAndHistory 组装顺序:引导/指令 → 片段 → 工具名清单;历史原样附于 system 之后。
// 同时守护 M1 去重:提示词内**不得**出现工具 description/schema(结构化下发已含,重复即双重计费)。
func TestAssembleOrderToolsAndHistory(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	svc, _ := startPrompt(t, nil)
	svc.AddSection(sdk.SystemPromptSection{Name: "能力", Content: func() string { return "片段内容" }})

	hist := []sdk.LLMMessage{
		{Role: sdk.RoleUser, Content: "历史用户消息"},
		{Role: sdk.RoleAssistant, Content: "历史助手消息"},
	}
	tools := []sdk.ToolDefinition{{
		Name:        "read",
		Description: "读文件",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
	}}
	msgs := svc.Assemble(hist, tools)
	if len(msgs) != 3 || msgs[0].Role != sdk.RoleSystem {
		t.Fatalf("应为 system + 2 条历史: %+v", msgs)
	}
	if msgs[1].Content != "历史用户消息" || msgs[2].Content != "历史助手消息" {
		t.Fatalf("历史应原样附于 system 之后: %+v", msgs)
	}
	sys := msgs[0].Content
	iSec := strings.Index(sys, "片段内容")
	iTools := strings.Index(sys, "可用工具:")
	if iSec < 0 || iTools < 0 || iSec > iTools {
		t.Fatalf("片段应先于工具名清单:\n%s", sys)
	}
	if !strings.Contains(sys, "可用工具:read") {
		t.Fatalf("工具名清单应含名称:\n%s", sys)
	}
	for _, dup := range []string{"inputSchema", "读文件", `"properties"`} {
		if strings.Contains(sys, dup) {
			t.Fatalf("工具 description/schema 不得写入提示词(已结构化下发,重复即双重计费),命中 %q:\n%s", dup, sys)
		}
	}
}

// TestAssemblePromptIndependentOfToolSchemaSize 提示词体积与工具 schema 体量解耦:
// 同一工具名配微小/巨大 schema,system 内容必须逐字节相同(去重生效的硬证明)。
func TestAssemblePromptIndependentOfToolSchemaSize(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	svc, _ := startPrompt(t, nil)

	fat := map[string]any{"type": "object"}
	props := map[string]any{}
	for i := 0; i < 200; i++ {
		props[fmt.Sprintf("field_%d", i)] = map[string]any{
			"type":        "string",
			"description": strings.Repeat("冗长描述", 40),
		}
	}
	fat["properties"] = props

	lean := svc.Assemble(nil, []sdk.ToolDefinition{{Name: "t", InputSchema: map[string]any{"type": "object"}}})
	heavy := svc.Assemble(nil, []sdk.ToolDefinition{{Name: "t", InputSchema: fat}})
	if len(lean) != 1 || len(heavy) != 1 {
		t.Fatalf("应各只有 system: %d/%d", len(lean), len(heavy))
	}
	if lean[0].Content != heavy[0].Content {
		t.Fatalf("提示词随 schema 变长 → 去重未生效(lean %d 字节 / heavy %d 字节)",
			len(lean[0].Content), len(heavy[0].Content))
	}
}

// TestAssembleNilHistoryAndEmptySection 片段内容为空/历史为 nil 时不产生空块与多余消息。
func TestAssembleNilHistoryAndEmptySection(t *testing.T) {
	s := &Service{}
	s.AddSection(sdk.SystemPromptSection{Name: "空段", Content: func() string { return "   " }})
	msgs := s.Assemble(nil, nil)
	if len(msgs) != 1 {
		t.Fatalf("无历史时应只有 system: %+v", msgs)
	}
	if !strings.Contains(msgs[0].Content, "空段:\n") {
		t.Fatalf("片段标题应保留(内容裁空):\n%s", msgs[0].Content)
	}
	if strings.Contains(msgs[0].Content, "项目指令(AGENTS.md") {
		t.Fatalf("空 projectInstr 不应注入块:\n%s", msgs[0].Content)
	}
}

// TestDefaultInstrCfg 缺省配置:全局与项目指令都开、无附加。
func TestDefaultInstrCfg(t *testing.T) {
	cfg := defaultInstrCfg()
	if !cfg.global || !cfg.project {
		t.Fatalf("缺省应 global/project 均开: %+v", cfg)
	}
	if len(cfg.extra) != 0 {
		t.Fatalf("缺省不应有附加指令: %+v", cfg.extra)
	}
}

// TestReloadRebuildsExtraInstructions /reload 时附加指令列表按 cfg 重建:
// 文件消失 → 从列表移除(而非留旧值);重新出现 → 回到列表。
func TestReloadRebuildsExtraInstructions(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	p1 := filepath.Join(dir, "one.md")
	p2 := filepath.Join(dir, "two.md")
	if err := os.WriteFile(p1, []byte("第一份附加"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p2, []byte("第二份附加"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Service{cfg: instrCfg{extra: []string{p1, p2}}}
	s.loadLocked()
	if len(s.extraInstr) != 2 {
		t.Fatalf("启动应读入两份: %+v", s.extraInstr)
	}

	if err := os.Remove(p2); err != nil {
		t.Fatal(err)
	}
	if err := s.ReloadInstructions(); err != nil {
		t.Fatalf("reload 应成功: %v", err)
	}
	if len(s.extraInstr) != 1 || s.extraInstr[0] != "第一份附加" {
		t.Fatalf("reload 应重建列表(移除已删文件): %+v", s.extraInstr)
	}
	sys := s.Assemble(nil, nil)[0].Content
	if strings.Contains(sys, "第二份附加") {
		t.Fatalf("已删文件的旧值不应残留:\n%s", sys)
	}

	if err := os.WriteFile(p2, []byte("第二份附加"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.ReloadInstructions(); err != nil {
		t.Fatal(err)
	}
	if len(s.extraInstr) != 2 {
		t.Fatalf("文件回来后 reload 应重新纳入: %+v", s.extraInstr)
	}
}
