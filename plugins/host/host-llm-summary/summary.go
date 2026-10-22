// Package hostllmsummary 提供 host-llm-summary 插件:滚动摘要的**模型摘要**实现。
//
// 它是 sdk.SessionSummarizer 的一个实现(可替换):token-compress 在折叠之后
// 把「已有累计摘要 + 本次新折叠的片段」交给它,产出**结构化**摘要
// (改过哪些文件 / 什么决定 / 未完成什么 / 什么错怎么解),替换掉抽取式摘要。
//
// 纪律(与 host-session-summary 同款,别各自发明):
//   - **输入有预算**:已有摘要 + 新片段合计 8KB(多出来的按"尾"取舍,并注明省略);
//   - **先脱敏再进模型**:凭据字面量不进模型上下文(internal/redact,单一事实源);
//   - **严格 JSON**:解析失败重试 1 次,仍失败 ⇒ 报错(调用方保持抽取式 = 确定性回退);
//   - **不许让回合失败**:任何失败都只表现为"没有升级",抽取式摘要继续用;
//   - **输出限长**:摘要进模型上下文,必须限在调用方给的 MaxChars 内。
//
// 为什么是独立插件而不是压缩引擎的一部分:压缩策略应当**可替换**(对照 pi/dsh/Codex
// 的做法)。谁提供 ctx.sessionSummarizer 就用谁;没人提供时 token-compress 纯抽取式。
package hostllmsummary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/redact"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-llm-summary。
type Plugin struct{}

// Name 返回插件 id。
func (p *Plugin) Name() string { return "host-llm-summary" }

// Options 生成策略(manifest data 覆盖)。
type Options struct {
	InputBudget int // 输入字节预算(默认 8192)
	MaxTokens   int // 输出 token 上限(默认 800)
	MaxRetries  int // JSON 解析失败重试次数(默认 1)
}

func defaultOptions() Options {
	return Options{InputBudget: 8192, MaxTokens: 800, MaxRetries: 1}
}

// Service 实现 sdk.SessionSummarizer。
type Service struct {
	ll sdk.LLMService
	o  Options
}

// Start 提供 ctx.sessionSummarizer。
//
// ctx.llm 是**可选**依赖:没有模型时摘要器照常注册,但 Summarize 报"不可用"
// —— 调用方据此保持抽取式摘要(确定性回退)。这不是"静默降级":降级路径是**显式**
// 的(错误 → 不写摘要),且状态可查(Summarize 返回错误)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var ll sdk.LLMService
	_ = c.Inject("ctx.llm", &ll) // 可选:缺失 ⇒ 摘要不可用(回退抽取式)
	o := defaultOptions()
	if m != nil {
		applyData(&o, m.Data)
	}
	s := &Service{ll: ll, o: o}
	if err := c.Provide("ctx.sessionSummarizer", s); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// applyData manifest data 覆盖(部分覆盖,其余取默认)。
func applyData(o *Options, data map[string]any) {
	if data == nil {
		return
	}
	get := func(k string) (int64, bool) {
		switch v := data[k].(type) {
		case int:
			return int64(v), true
		case int64:
			return v, true
		case float64:
			return int64(v), true
		}
		return 0, false
	}
	for _, k := range []struct {
		key string
		dst *int
	}{
		{"input_budget_bytes", &o.InputBudget},
		{"summary_max_tokens", &o.MaxTokens},
		{"max_retries", &o.MaxRetries},
	} {
		if v, ok := get(k.key); ok && v > 0 {
			*k.dst = int(v)
		}
	}
}

// Summarize 实现 sdk.SessionSummarizer:把「已有摘要 + 新片段」合并成结构化摘要。
func (s *Service) Summarize(ctx context.Context, in sdk.SummaryRequest) (string, error) {
	if s.ll == nil {
		return "", errors.New("模型不可用(ctx.llm 未装配)")
	}
	prompt := buildPrompt(in, s.o.InputBudget)
	maxTok := s.o.MaxTokens
	req := &sdk.LLMRequest{
		Model: s.ll.Model(),
		Messages: []sdk.LLMMessage{
			{Role: sdk.RoleSystem, Content: systemPrompt},
			{Role: sdk.RoleUser, Content: prompt},
		},
		MaxTokens:   &maxTok,
		Temperature: floatPtr(0.2),
		// 思考档**显式关掉**:这是后台补写,不需要推理;不显式关的话,host-llm 会把
		// 会话级的思考档回填进来(可能是一边推理一边写摘要 = 白烧 token)。
		Thinking:    sdk.ThinkingOff,
		ThinkingSet: true,
	}
	var lastErr error
	for attempt := 0; attempt <= s.o.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		resp, err := s.ll.Complete(ctx, req, nil)
		if err != nil {
			return "", fmt.Errorf("摘要模型调用失败: %w", err)
		}
		out, perr := parseOut(resp.Message.Content)
		if perr == nil {
			text := render(out)
			if strings.TrimSpace(text) == "" {
				return "", errors.New("摘要内容为空")
			}
			return clip(text, in.MaxChars), nil
		}
		lastErr = perr
		req.Messages = append(req.Messages,
			sdk.LLMMessage{Role: sdk.RoleUser, Content: "输出不是合法 JSON(" + perr.Error() + ")。请只输出 JSON 对象,不要任何解释或代码围栏。"})
	}
	return "", fmt.Errorf("摘要输出无法解析为 JSON: %w", lastErr)
}

// systemPrompt 固定系统提示(严格 JSON、中文、不编造)。
//
// 首行是 sdk.LLMSummaryMarker:让脚本化适配器(llm-mock)与测试探针能认出
// 「这是后台摘要请求,不是回合请求」—— 否则它会消耗回合的脚本步 / 污染请求计数。
const systemPrompt = sdk.LLMSummaryMarker + `你是会话归档助手。把「已有摘要」与「新增片段」合并成一份**结构化**摘要,输出严格 JSON(不要代码围栏、不要解释):
{"task":"当前任务(一句话,≤60字)","changes":["改过的文件/接口/配置(≤5条)"],"decisions":["关键决定(≤5条)"],"open":["未完成项(≤5条)"],"errors":["错误与解法(≤3条)"]}
要求:中文;只依据给定内容,不编造;没有的项给空数组;不要输出 JSON 以外的任何文字。`

// out 模型返回的摘要 JSON。
type out struct {
	Task      string   `json:"task"`
	Changes   []string `json:"changes"`
	Decisions []string `json:"decisions"`
	Open      []string `json:"open"`
	Errors    []string `json:"errors"`
}

// buildPrompt 构造用户提示(先脱敏;超预算按"尾"取舍并注明省略)。
func buildPrompt(in sdk.SummaryRequest, budget int) string {
	prev := redact.Text(in.Prev)
	seg := redact.Text(in.Seg)
	// 预算分配:新片段优先(它是本次新折进来的内容),已有摘要次之。
	segBudget := budget / 2
	prevBudget := budget - segBudget
	if len(seg) > segBudget {
		seg = trimTail(seg, segBudget)
	}
	if len(prev) > prevBudget {
		prev = trimTail(prev, prevBudget)
	}
	var b strings.Builder
	b.WriteString("【已有摘要】\n")
	if strings.TrimSpace(prev) == "" {
		b.WriteString("(无)\n")
	} else {
		b.WriteString(prev)
		if !strings.HasSuffix(prev, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString("【新增片段】\n")
	b.WriteString(seg)
	return b.String()
}

// trimTail 保留尾部 n 字节(不切碎 UTF-8),并在前头注明省略。
func trimTail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	b := []byte(s)[len(s)-n:]
	for len(b) > 0 {
		if r := b[0]; r < 0x80 || r >= 0xC0 {
			break
		}
		b = b[1:]
	}
	return "[前文省略]\n" + string(b)
}

// parseOut 严格解析模型输出(容忍代码围栏/前后空白)。
func parseOut(s string) (out, error) {
	t := strings.TrimSpace(s)
	if i := strings.Index(t, "```"); i >= 0 {
		t = t[i+3:]
		if j := strings.Index(t, "```"); j >= 0 {
			t = t[:j]
		}
		t = strings.TrimPrefix(strings.TrimSpace(t), "json")
	}
	t = strings.TrimSpace(t)
	start, end := strings.Index(t, "{"), strings.LastIndex(t, "}")
	if start < 0 || end <= start {
		return out{}, errors.New("未找到 JSON 对象")
	}
	var o out
	if err := json.Unmarshal([]byte(t[start:end+1]), &o); err != nil {
		return out{}, err
	}
	o.Task = strings.TrimSpace(o.Task)
	return o, nil
}

// render 渲染成给模型看的结构化文本(空项不出现;每项一行,便于模型扫读)。
func render(o out) string {
	var b strings.Builder
	if o.Task != "" {
		b.WriteString("任务: " + clip(o.Task, 120) + "\n")
	}
	for _, sec := range []struct {
		label string
		items []string
	}{
		{"改动", o.Changes},
		{"决定", o.Decisions},
		{"未完成", o.Open},
		{"错误", o.Errors},
	} {
		items := clean(sec.items)
		if len(items) == 0 {
			continue
		}
		b.WriteString(sec.label + ": " + strings.Join(items, "；") + "\n")
	}
	return b.String()
}

// clean 去空项、去重、限条数(≤5)。
func clean(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" || seen[it] {
			continue
		}
		seen[it] = true
		out = append(out, it)
		if len(out) >= 5 {
			break
		}
	}
	return out
}

// clip 按 rune 截断并加省略号。
func clip(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func floatPtr(f float64) *float64 { return &f }
