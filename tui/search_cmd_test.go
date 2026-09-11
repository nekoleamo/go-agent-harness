// /search 命令交互链路回归测试:注册表必须声明自由参数级(选中后断点输入搜索词,
// 输入词回车才执行)。曾缺 Args → 选中 /search 即提交(无参报错),随后输入的文字
// 被当作普通消息发给大模型,搜索功能表现为"无效"。见 DESIGN.md §14.1 M6.20。
package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 测试替身:最小 Ctx / LLMService / AgentLoop / CommandRegistry ——

type stubCtx struct {
	svc map[string]any
}

func (s *stubCtx) Provide(key string, svc any) error { s.svc[key] = svc; return nil }
func (s *stubCtx) Inject(key string, out any) error {
	v, ok := s.svc[key]
	if !ok {
		return fmt.Errorf("stub: 无服务 %s", key)
	}
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("stub: Inject 目标需非空指针: %T", out)
	}
	sv := reflect.ValueOf(v)
	if !sv.Type().AssignableTo(rv.Elem().Type()) {
		return fmt.Errorf("stub: 服务 %s 类型不匹配(有 %T,要 %T)", key, v, out)
	}
	rv.Elem().Set(sv)
	return nil
}
func (s *stubCtx) Subscribe(string, sdk.AnyListener) sdk.Disposer { return func() {} }
func (s *stubCtx) Emit(context.Context, string, any, sdk.DispatchMode) (any, error) {
	return nil, nil
}
func (s *stubCtx) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type stubLLM struct{}

func (stubLLM) RegisterAdapter(sdk.LLMAdapter) sdk.Disposer { return func() {} }
func (stubLLM) SetModel(string)                             {}
func (stubLLM) Complete(context.Context, *sdk.LLMRequest, func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	return nil, nil
}
func (stubLLM) Model() string                        { return "mock" }
func (stubLLM) List() []string                       { return nil }
func (stubLLM) SetProvider(string, string) error     { return nil }
func (stubLLM) UnsetProvider(string) error           { return nil }
func (stubLLM) ResetProvider() error                 { return nil }
func (stubLLM) ProviderInfo() (string, string, bool) { return "", "", false }
func (stubLLM) ListModels() ([]sdk.ModelInfo, error) { return nil, nil }
func (stubLLM) SetThinking(sdk.ThinkingLevel)        {}
func (stubLLM) Thinking() sdk.ThinkingLevel          { return sdk.ThinkingOff }

type stubLoop struct{}

func (stubLoop) Run(context.Context, string) error { return nil }

// memRegistry 内存命令注册表(同名冲突拒绝,顺序稳定)。
type memRegistry struct {
	specs map[string]sdk.CommandSpec
	order []string
}

func newMemRegistry() *memRegistry {
	return &memRegistry{specs: map[string]sdk.CommandSpec{}}
}
func (r *memRegistry) Register(s sdk.CommandSpec) (sdk.Disposer, error) {
	if _, exists := r.specs[s.Name]; exists {
		return nil, fmt.Errorf("命令 /%s 已注册", s.Name)
	}
	r.specs[s.Name] = s
	r.order = append(r.order, s.Name)
	return func() {}, nil
}
func (r *memRegistry) List() []sdk.CommandSpec {
	out := make([]sdk.CommandSpec, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.specs[n])
	}
	return out
}
func (r *memRegistry) Get(n string) (sdk.CommandSpec, bool) {
	s, ok := r.specs[n]
	return s, ok
}

// commandTestApp 装配最小 App:stubCtx 注入内存命令注册表,NewApp 内部
// registerInternalCommands 已注册 /search 等内部命令。
func commandTestApp() *App {
	reg := newMemRegistry()
	c := &stubCtx{svc: map[string]any{"ctx.commands": reg}}
	return NewApp(c, stubLoop{}, stubLLM{}, "tui")
}

// TestSearchCommandFreeArgDeclared /search 必须声明自由参数级(FreeArgs):
// 这是断点输入的前提;缺 Args 时选中即 Commit 提交空参数报错(根因)。
func TestSearchCommandFreeArgDeclared(t *testing.T) {
	a := commandTestApp()
	spec, ok := a.cmds.Get("search")
	if !ok {
		t.Fatal("/search 应已注册")
	}
	if len(spec.Args) == 0 || spec.Args[0].FreeArgs == nil {
		t.Fatal("/search 必须声明自由参数级 Args[0].FreeArgs(选中后断点输入搜索词)")
	}
	if len(spec.Args[0].FreeArgs(nil)) == 0 {
		t.Fatal("自由级应提示参数名(搜索词)")
	}
}

// TestSearchCommandFlowBreakThenCommit 完整用户流回归:输入 / 选中 search 回车 →
// 断点(不执行、光标回输入框、提示继续输入)→ 输入词回车 → 真正进入搜索态。
// 曾因缺 Args:选中即提交报错,再输入的词走了普通消息通道发给大模型。
func TestSearchCommandFlowBreakThenCommit(t *testing.T) {
	a := commandTestApp()
	m := a.model
	m.w, m.h = 60, 20
	m.state.sessionWin = 17
	m.state.Lines = append(m.state.Lines,
		Line{Kind: "user", Text: "请解释 keyword 机制"},
		Line{Kind: "assistant", Text: "keyword 相关内容如下"},
	)

	// 1. 输入 / → 选择器激活(全部命令)
	m.state.Input = "/"
	m.syncHints()
	if m.state.Pick == nil || len(m.state.Pick.Items) == 0 {
		t.Fatal("输入 / 应激活命令选择器")
	}
	idx := -1
	for i, it := range m.state.Pick.Items {
		if it.Value == "search" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("选择器应包含 search 选项")
	}

	// 2. 回车选中 /search → 自由级断点:不执行,输入框保留 /search,提示继续输入
	m.state.Pick.Cursor = idx
	m.enter()
	if m.state.SearchQuery != "" {
		t.Fatalf("选中回车不应直接执行搜索(应断点等待输入词): q=%q", m.state.SearchQuery)
	}
	if m.state.Input != "/search " {
		t.Fatalf("断点应保留 /search 与尾随空格(直接打字即词): %q", m.state.Input)
	}
	hint := strings.Join(m.state.Suggestions, " ")
	if !strings.Contains(hint, "搜索词") {
		t.Fatalf("断点应提示\"继续输入 搜索词\": %q", m.state.Suggestions)
	}

	// 3. 输入搜索词后回车 → 提交 /search keyword → 进入搜索态(命中 2 行)
	for _, r := range "keyword" {
		m.state.InsertRune(r)
	}
	m.syncHints()
	if m.state.Input != "/search keyword" {
		t.Fatalf("输入词后应拼为 /search keyword: %q", m.state.Input)
	}
	m.enter()
	if m.state.Error != "" {
		t.Fatalf("不应产生错误: %s", m.state.Error)
	}
	if m.state.SearchQuery != "keyword" {
		t.Fatalf("提交后应进入搜索态: q=%q", m.state.SearchQuery)
	}
	if len(m.state.SearchHits) != 2 || m.state.SearchHits[0] != 0 || m.state.SearchHits[1] != 1 {
		t.Fatalf("应命中 2 行(0/1): %v", m.state.SearchHits)
	}
	// 输入框已清空、回执作为 meta 行上屏
	if m.state.Input != "" {
		t.Fatalf("提交后输入框应清空: %q", m.state.Input)
	}
	if !strings.Contains(m.state.Lines[len(m.state.Lines)-1].Text, "命中 2 行") {
		t.Fatalf("应显示命中回执 meta 行: %+v", m.state.Lines[len(m.state.Lines)-1])
	}
}
