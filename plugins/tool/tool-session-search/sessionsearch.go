// Package toolsessionsearch 提供 tool-session-search 插件(S-P2-5):跨会话检索工具。
//
// 为什么需要它:会话账本已经是完整的事实流水(每次 user/assistant/工具调用都落
// `$GAH_HOME/sessions/*.jsonl`),但此前只有「当前会话内」的 TUI `/search` 和浏览器
// 搜索。模型遇到「这个问题以前修过吗」「上次那个报错怎么解的」只能靠用户回忆 ——
// 本工具把历史会话变成可检索的事实面(对应 dsh `session_search` / Hermes FTS5 的位置,
// 但**不引依赖、不建索引**:线性扫描 + 硬预算,见 search.go 包注释)。
//
// 三条口径纪律:
//  1. **默认只在当前工作区内检索**(scope=workspace):跨项目内容不因一次工具调用进模型
//     上下文;要跨项目必须显式 scope=all。
//  2. **命中是历史记录,不是当前事实**:输出里带会话文件/时间,由模型自己判断是否仍然成立
//     (与 AGENTS.md「历史回忆只作参考非事实依据」一致)。
//  3. **预算用尽必须可见**:扫描被截断时结果带 truncated + 原因,绝不让调用方以为
//     「没搜到 = 不存在」(S-P1-2 同款诚实口径)。
//
// 只读:不写任何文件、不新增写盘路径(便携纪律下零风险)。
package toolsessionsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

const (
	// ToolName 工具名(与 DESIGN S-P2-5 登记一致)。
	ToolName = "session_search"
	// defaultLimit 默认返回会话数。
	defaultLimit = 5
	// maxLimit 单次返回会话数上限(防一次把上下文灌满)。
	maxLimit = 20
)

// Tool 实现 sdk.Tool:跨会话检索。
type Tool struct{}

// NewTool 外部化工厂(extplugins/tool-basic 注册用)。
func NewTool() sdk.Tool { return &Tool{} }

// Plugin 实现 tool-session-search(双轨:装配进宿主时经 ctx.tools 注册;默认配置里
// 内嵌实现停用,由 host-bridge 加载 extplugins/tool-basic,与 tool-files/tool-todo 同模式)。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-session-search" }

func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	return tools.Register(NewTool()), nil
}

// Definition 工具声明(TimeoutMs 必填:桥默认超时 3s 对多文件扫描不够)。
func (t *Tool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: ToolName,
		Description: "检索历史会话(跨会话,不限于当前会话)。用于「这个问题以前解决过吗」「上次那个报错怎么说的」" +
			"「哪个会话改过这个文件」这类回忆性问题;返回命中片段 + 会话文件/时间/显示名/概述,可据此让用户切到那个会话。" +
			"query 空白分隔多词(全部命中会提权);query 留空 = 只看最近会话列表。" +
			"默认只搜当前工作区的会话(scope=all 才跨工作区,会读入其它项目的内容,仅在用户要求时用)。" +
			"命中内容是历史记录而非当前事实,引用前先核对现状;没搜到不等于不存在(结果 truncated=true 时说明预算用尽)。",
		TimeoutMs: 20000,
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"query"},
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "查询词(空白分隔;留空 = 最近会话列表)"},
				"limit": map[string]any{"type": "integer", "description": "返回几个会话(默认 5,上限 20)"},
				"scope": map[string]any{"type": "string", "enum": []string{"workspace", "all"},
					"description": "workspace(默认,只搜当前工作区)| all(全部工作区)"},
			},
		},
	}
}

// args 入参。
type args struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
	Scope string `json:"scope"`
}

// Execute 执行检索。参数错误显式报错;数据缺失/为空走 Notes 说明(不静默)。
func (t *Tool) Execute(_ context.Context, raw string) (any, error) {
	var a args
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &a); err != nil {
		return nil, fmt.Errorf("%s: args: %w", ToolName, err)
	}
	res, err := Search(Options{Query: a.Query, Limit: a.Limit, Scope: Scope(strings.ToLower(strings.TrimSpace(a.Scope)))})
	if err != nil {
		return map[string]any{"error": ToolName + ": " + err.Error()}, nil
	}
	return res, nil
}
