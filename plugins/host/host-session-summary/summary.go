// Package hostsummary 提供 host-session-summary 插件(F 组 F3):会话概述(LLM 总结)。
//
// 产物 = title(可反哺会话名)+ summary(一句话)+ topics(≤3 主题词),落 meta.json 缓存
// (经 host-cwd-sessions 的元数据存储)。
//
// 纪律(docs/SESSION_UX_PLAN.md §5):
//   - **列表请求绝不触发模型**:只有手动(UI 动作)与"回合后自动"(K3 默认开)两档;
//   - 全局单飞(max 1 in-flight)+ 每会话节流 ≥60s + 输入 8KB 预算(首 2 轮 + 最近 6 轮);
//   - 严格 JSON 输出,解析失败重试 1 次,仍失败 → unavailable(不写垃圾);
//   - 概述只落 meta.json,**不落会话 jsonl、不进模型上下文**(纯展示);
//   - 隐私:输入先过脱敏(与工具子进程同款凭据过滤思路:剥离常见密钥字面量),首次生成由 UI 提示。
package hostsummary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-session-summary。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-session-summary" }

// Options 生成策略(manifest data 覆盖)。
type Options struct {
	AutoSummary   bool // 回合后自动生成(默认开;K3)
	MinTurns      int  // 触发自动生成的最小用户轮数(默认 2)
	StaleFrames   int  // 覆盖帧数落后多少触发(默认 8)
	MinInterval   int  // 每会话节流秒数(默认 600)
	InputBudget   int  // 输入字节预算(默认 8192)
	FirstTurns    int  // 保留的最早轮数(默认 2)
	LastTurns     int  // 保留的最近轮数(默认 6)
	MaxRetries    int  // JSON 解析失败重试次数(默认 1)
	SummaryTokens int  // 概述输出上限 token(默认 400)
	DisableAutoAt bool // 显式关闭自动档(等价 auto_summary=false)
}

func defaultOptions() Options {
	return Options{AutoSummary: true, MinTurns: 2, StaleFrames: 8, MinInterval: 600,
		InputBudget: 8192, FirstTurns: 2, LastTurns: 6, MaxRetries: 1, SummaryTokens: 400}
}

// Service 实现 sdk.SessionSummaryService。
type Service struct {
	c  sdk.Ctx
	ll sdk.LLMService
	cs sdk.CwdSessions
	o  Options

	mu       sync.Mutex
	inflight bool
	lastGen  map[string]time.Time // 会话文件 → 上次生成时刻(节流)
	lastErr  map[string]string    // 会话文件 → 最近失败原因(展示)
}

// Start 提供 ctx.sessionSummary。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var ll sdk.LLMService
	_ = c.Inject("ctx.llm", &ll) // 可选:缺失 → Summary 报 unavailable
	var cs sdk.CwdSessions
	if err := c.Inject("ctx.cwdSessions", &cs); err != nil {
		return nil, fmt.Errorf("host-session-summary 需要 ctx.cwdSessions: %w", err)
	}
	o := defaultOptions()
	if m != nil {
		applyData(&o, m.Data)
	}
	s := &Service{c: c, ll: ll, cs: cs, o: o, lastGen: map[string]time.Time{}, lastErr: map[string]string{}}
	if err := c.Provide("ctx.sessionSummary", s); err != nil {
		return nil, err
	}
	// 回合后自动生成(K3 默认开):订阅 turn/end,后台异步(不阻塞回合收尾)
	var dis sdk.Disposer = func() {}
	if o.AutoSummary {
		dis = c.Subscribe(sdk.EventTurnEnd, func(_ context.Context, _ *sdk.Event) error {
			go s.autoAfterTurn()
			return nil
		})
	}
	return func() { dis() }, nil
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
	if v, ok := data["auto_summary"].(bool); ok {
		o.AutoSummary = v
	}
	for _, k := range []struct {
		key string
		dst *int
	}{
		{"min_turns", &o.MinTurns}, {"stale_frames", &o.StaleFrames},
		{"min_interval_seconds", &o.MinInterval}, {"input_budget_bytes", &o.InputBudget},
		{"first_turns", &o.FirstTurns}, {"last_turns", &o.LastTurns},
		{"max_retries", &o.MaxRetries}, {"summary_max_tokens", &o.SummaryTokens},
	} {
		if v, ok := get(k.key); ok && v > 0 {
			*k.dst = int(v)
		}
	}
}

// AutoEnabled 「回合后自动生成」是否开启。
func (s *Service) AutoEnabled() bool { return s.o.AutoSummary }

// autoAfterTurn 回合结束后的自动生成判定(节流 + 帧数落后阈值;全局单飞)。
func (s *Service) autoAfterTurn() {
	if !s.o.AutoSummary {
		return
	}
	id := s.cs.CurrentSession()
	info, ok := s.infoOf(id)
	if !ok {
		return
	}
	// 最小轮数:用户消息 < MinTurns 的短会话不生成(信息量不足)
	if userTurns(info.Path) < s.o.MinTurns {
		return
	}
	s.mu.Lock()
	inflight := s.inflight
	last := s.lastGen[filepath.Base(info.Path)]
	s.mu.Unlock()
	if inflight {
		return
	}
	if !last.IsZero() && time.Since(last) < time.Duration(s.o.MinInterval)*time.Second {
		return
	}
	// 缓存仍新鲜(已有概述且落后帧数未达阈值)则跳过
	if info.Summary != "" && info.Frames-info.SummaryCoveredFrames < s.o.StaleFrames {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := s.Summary(ctx, id, false); err != nil {
		// 自动档失败不打扰用户(记录原因供状态展示)
		s.mu.Lock()
		s.lastErr[filepath.Base(info.Path)] = err.Error()
		s.mu.Unlock()
	}
}

// infoOf 取会话信息(id 空 = 主会话;找不到 → false)。
func (s *Service) infoOf(id string) (sdk.SessionInfo, bool) {
	for _, si := range s.cs.Sessions() {
		if si.ID == id {
			return si, true
		}
	}
	return sdk.SessionInfo{}, false
}

// Summary 取回或生成概述(id 空 = 主会话;force 忽略缓存)。
// 纪律:调用模型前先查缓存(未 force 且 state=ready 直接返回);全局单飞。
func (s *Service) Summary(ctx context.Context, id string, force bool) (sdk.SessionSummary, error) {
	info, ok := s.infoOf(id)
	if !ok {
		return sdk.SessionSummary{}, fmt.Errorf("会话不存在: %q", id)
	}
	if !force && info.SummaryState == "ready" && info.Summary != "" {
		return sdk.SessionSummary{Text: info.Summary, Topics: info.SummaryTopics,
			CoveredFrames: info.Frames, Model: s.ll.Model()}, nil
	}
	if s.ll == nil {
		return sdk.SessionSummary{}, errors.New("概述不可用(ctx.llm 未装配)")
	}
	s.mu.Lock()
	if s.inflight {
		s.mu.Unlock()
		return sdk.SessionSummary{}, errors.New("概述生成中(全局单飞,请稍候)")
	}
	s.inflight = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inflight = false
		s.mu.Unlock()
	}()

	input, turns, err := buildInput(info.Path, s.o)
	if err != nil {
		return sdk.SessionSummary{}, err
	}
	if turns < s.o.MinTurns {
		return sdk.SessionSummary{}, fmt.Errorf("会话轮次不足(%d < %d),无需概述", turns, s.o.MinTurns)
	}
	out, err := s.generate(ctx, input)
	if err != nil {
		s.mu.Lock()
		s.lastErr[filepath.Base(info.Path)] = err.Error()
		s.mu.Unlock()
		return sdk.SessionSummary{}, err
	}
	sum := sdk.SessionSummary{Text: out.Summary, Topics: out.Topics,
		CoveredFrames: info.Frames, Model: s.ll.Model(), TS: time.Now().Unix()}
	if err := s.cs.SetSummary(info.ID, sum); err != nil {
		return sdk.SessionSummary{}, fmt.Errorf("概述缓存写入失败: %w", err)
	}
	s.mu.Lock()
	s.lastGen[filepath.Base(info.Path)] = time.Now()
	delete(s.lastErr, filepath.Base(info.Path))
	s.mu.Unlock()
	// 标题回填会话名(仅未命名会话;绝不覆盖用户起的名字)
	if out.Title != "" && info.Name == "" {
		_ = s.cs.SetName(info.ID, out.Title)
	}
	return sum, nil
}

// genOut 模型返回的概述 JSON。
type genOut struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Topics  []string `json:"topics"`
}

// generate 调模型生成概述(严格 JSON;失败重试 MaxRetries 次)。
func (s *Service) generate(ctx context.Context, input string) (genOut, error) {
	maxTok := s.o.SummaryTokens
	req := &sdk.LLMRequest{
		Model: s.ll.Model(),
		Messages: []sdk.LLMMessage{
			{Role: sdk.RoleSystem, Content: summarySystemPrompt},
			{Role: sdk.RoleUser, Content: input},
		},
		MaxTokens:   &maxTok,
		Temperature: floatPtr(0.2),
	}
	var lastErr error
	for attempt := 0; attempt <= s.o.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return genOut{}, ctx.Err()
		}
		resp, err := s.ll.Complete(ctx, req, nil)
		if err != nil {
			return genOut{}, fmt.Errorf("概述模型调用失败: %w", err)
		}
		out, perr := parseSummary(resp.Message.Content)
		if perr == nil {
			return out, nil
		}
		lastErr = perr
		req.Messages = append(req.Messages,
			sdk.LLMMessage{Role: sdk.RoleUser, Content: "输出不是合法 JSON(" + perr.Error() + ")。请只输出 JSON 对象,不要任何解释或代码围栏。"})
	}
	return genOut{}, fmt.Errorf("概述输出无法解析为 JSON: %w", lastErr)
}

// summarySystemPrompt 固定系统提示(要求 JSON、中文、不编造)。
const summarySystemPrompt = `你是会话归档助手。阅读给定的会话片段,输出严格 JSON(不要代码围栏、不要解释):
{"title":"会话标题(≤20 字,概括任务)","summary":"一句话概述(≤60 字,说明做了什么/结论)","topics":["主题词1","主题词2","主题词3"]}
要求:中文;只依据会话内容,不编造;topics 最多 3 个、每个 ≤8 字;若信息不足,summary 写"信息不足,无法概述"。`

// parseSummary 严格解析模型输出(容忍代码围栏/前后空白)。
func parseSummary(s string) (genOut, error) {
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
		return genOut{}, errors.New("未找到 JSON 对象")
	}
	var out genOut
	if err := json.Unmarshal([]byte(t[start:end+1]), &out); err != nil {
		return genOut{}, err
	}
	out.Summary = strings.TrimSpace(out.Summary)
	out.Title = strings.TrimSpace(out.Title)
	if out.Summary == "" {
		return genOut{}, errors.New("summary 为空")
	}
	out.Summary = truncRunes(out.Summary, 60)
	out.Title = truncRunes(out.Title, 20)
	if len(out.Topics) > 3 {
		out.Topics = out.Topics[:3]
	}
	for i := range out.Topics {
		out.Topics[i] = truncRunes(strings.TrimSpace(out.Topics[i]), 8)
	}
	return out, nil
}

func floatPtr(f float64) *float64 { return &f }

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
