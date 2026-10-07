// Package hostusagestats 提供 host-usage-stats 插件:ctx.usageStats 会话级 token 消耗统计。
// 只订阅 session/event 广播,从 session/usage 事件(SessionEvent 载荷 sdk.UsageEvent)累计——
// 数据源是 agent-loop 每轮 LLM 请求完成后记录的 Usage 事件(同日志留盘,不变量一致)。
// 零宿主依赖、零适配器依赖:卸载随 Disposer 撤销,可独立开关。
//
// 本包按职责拆两个文件:
//   - usagestats.go:插件装配 + 统计服务(累计/快照/重置/错误驱动学习入口);
//   - modelwindows.go:模型窗口知识库(窗口表 + 前缀匹配 + 错误文本解析,领域知识集中一处)。
//
// 上下文窗口解析链(见 modelwindows.go):context_window(统一覆盖)> model_windows(配置层)>
// 错误驱动学习(实测)> 内置表(发行基线)> 0(未知)。未知/空模型窗口=0:
// 状态栏只显示使用量,不显示总量/百分比(不假精确)。
package hostusagestats

import (
	"context"
	"strings"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-usage-stats。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-usage-stats" }

// Start 提供 ctx.usageStats 并订阅事件累计 + 错误驱动学习。
// data.context_window:统一窗口覆盖(>0 时所有模型用该值);
// data.model_windows:按模型名前缀的配置层覆盖(map[前缀→窗口],新模型无需改代码)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	svc := &Service{}
	if m != nil && m.Data != nil {
		if w, ok := m.Data["context_window"].(int); ok && w > 0 {
			svc.windowOverride = w // 显式覆盖:所有模型统一用该值
		}
		if mw, ok := m.Data["model_windows"].(map[string]any); ok {
			svc.extraWindows = make(map[string]int, len(mw))
			for k, v := range mw {
				if i, ok := v.(int); ok && i > 0 {
					svc.extraWindows[k] = i
				}
			}
		}
	}
	if err := c.Provide("ctx.usageStats", svc); err != nil {
		return nil, err
	}
	d := c.Subscribe(sdk.EventSession, func(ctx context.Context, ev *sdk.Event) error {
		if sev, ok := ev.Payload.(*sdk.SessionEvent); ok {
			svc.HandleSessionEvent(sev)
		}
		// 窗口快照广播(每轮 usage 后):压缩阈值要按窗口比例定,而窗口解析链只在本插件。
		// 走事件而非 Inject:订阅回调不在锁内(总线分发前已拷出监听器),嵌套 Emit 安全;
		// 且消费方与本插件的装配顺序无关。
		if sev, ok := ev.Payload.(*sdk.SessionEvent); ok && sev.Kind == sdk.EventUsage {
			_, _ = c.Emit(ctx, sdk.EventUsageWindow, svc.currentWindow(), sdk.Emit)
		}
		return nil
	})
	// 实例级用量事件:按会话分桶记账(账本仍在会话日志里,这条只是通知通道)。
	d3 := c.Subscribe(sdk.EventUsageRecorded, func(_ context.Context, ev *sdk.Event) error {
		ue, ok := ev.Payload.(sdk.UsageEvent)
		if !ok {
			return nil
		}
		svc.add(ue.Session, ue.Model, ue.Usage)
		_, _ = c.Emit(context.Background(), sdk.EventUsageWindow, svc.currentWindow(), sdk.Emit)
		return nil
	})
	// 错误驱动学习:LLM 请求超窗口失败时,错误文本携带该模型窗口数字,
	// 解析后记忆(新模型无需改表/配置即自动获取窗口,见 modelwindows.go)。
	d2 := c.Subscribe(sdk.EventAgentError, func(ctx context.Context, ev *sdk.Event) error {
		switch p := ev.Payload.(type) {
		case *sdk.LLMError:
			svc.LearnWindowFromError(p.Model, p.Err.Error())
		case error:
			svc.LearnWindowFromError("", p.Error())
		}
		return nil
	})
	return func() { d(); d2(); d3() }, nil
}

// counter 一个会话的累计值(按会话分桶,见 Service.buckets)。
type counter struct {
	model              string
	prompt, completion int
	lastPrompt         int // 最近一次请求的实测输入 token(上下文占用口径,不是累计)
	cached             int
	requests           int
}

// Service 实现 sdk.UsageStatsService:按会话分桶的累计统计。
//
// **为什么分桶**(第一百一十六批):原先是一个全局累加器 + 切会话 Reset。多会话并行下这是错的 ——
// 页签 A 在跑、页签 B 切一下,A 的 token 数当场归零;而且非主会话的 usage 事件走
// `session/event/<id>`,本服务只订阅主会话事件名,压根收不到。
//
// 桶的键 = 会话 id(空 = 主会话,与 sessionKey 同口径)。桶只增不减(会话删了桶还在):
// 内存占用是每桶几十字节、生命周期等于进程,不值得为它引入清理路径。
type Service struct {
	mu             sync.Mutex
	windowOverride int            // data.context_window 显式覆盖(0 = 未配置,按模型解析)
	extraWindows   map[string]int // data.model_windows 配置层覆盖(前缀→窗口)
	learned        map[string]int // 错误驱动学习缓存(模型精确名 → 实测窗口;按模型,不分会话)
	buckets        map[string]*counter
	cur            string // 最近一次累加的会话 id(供无参 Stats() 回落;单会话下即主会话)
}

// HandleSessionEvent 事件过滤:仅 session/usage 载荷累计(订阅回调调用;纯逻辑可测)。
// 每次事件还按携带模型刷新窗口(模型切换自动跟随;显式覆盖优先)。
func (s *Service) HandleSessionEvent(sev *sdk.SessionEvent) {
	if sev == nil || sev.Kind != sdk.EventUsage {
		return
	}
	ue, ok := sev.Payload.(sdk.UsageEvent)
	if !ok {
		// 兼容旧日志:载荷为裸 sdk.Usage(无模型名)时按现状累计,窗口不刷新
		if u, ok2 := sev.Payload.(sdk.Usage); ok2 {
			s.add("", "", u)
		}
		return
	}
	if ue.Session != "" {
		return // 带会话归属的由 usage/recorded 记账(agent-loop 同时发两条,这里跳过防重复计数)
	}
	// 无 Session 字段 = 老日志/主单例通道 ⇒ 归主会话桶(空键)。
	s.add("", ue.Model, ue.Usage)
}

// LearnWindowFromError 错误驱动学习:从 LLM 超限错误文本解析窗口数字并记忆
// (agent/error 订阅回调调用;模型为空或解析不到则忽略)。
// 新模型无需改表/配置:首次超限即自动获得窗口,后续展示直接带总量。
func (s *Service) LearnWindowFromError(model, msg string) {
	if model == "" {
		return
	}
	w := parseWindowFromError(msg)
	if w <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.learned == nil {
		s.learned = make(map[string]int)
	}
	s.learned[strings.ToLower(model)] = w
}

// add 累计一笔 usage 到该会话的桶(sid 空 = 主会话)。
func (s *Service) add(sid, model string, u sdk.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buckets == nil {
		s.buckets = make(map[string]*counter)
	}
	c := s.buckets[sid]
	if c == nil {
		c = &counter{}
		s.buckets[sid] = c
	}
	if model != "" {
		c.model = model
	}
	c.prompt += u.PromptTokens
	c.completion += u.CompletionTokens
	c.lastPrompt = u.PromptTokens // 上下文占用 = 最近一次发出去的 prompt,与累计量无关
	c.cached += u.CachedTokens
	c.requests++
	s.cur = sid
}

// currentWindow 当前窗口(无参 = 最近累加的那个会话的模型)。
func (s *Service) currentWindow() int {
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	return s.currentWindowFor(cur)
}

// currentWindowLocked 调用方已持锁的版本(StatsFor 里用)。
func (s *Service) currentWindowLocked() int { return s.currentWindowFor(s.cur) }

// currentWindowFor 指定会话的窗口:显式覆盖(context_window)> 按模型解析(见 modelwindows.go)> 0(未知)。
// 0 = 窗口未知:展示层只显示使用量。
//
// 注意:窗口是**按模型**解析的,不分会话 —— 同一个模型在哪用都是那个窗口;
// 会话没花过钱时用全局 cur 的模型兜底(展示上宁可给个大致值,也不要空白让人以为没窗口)。
func (s *Service) currentWindowFor(sessionID string) int {
	if s.windowOverride > 0 {
		return s.windowOverride
	}
	if c := s.buckets[sessionID]; c != nil && c.model != "" {
		return s.windowForModel(c.model)
	}
	if c := s.buckets[s.cur]; c != nil && c.model != "" {
		return s.windowForModel(c.model)
	}
	return 0
}

// Stats 累计统计快照(**最近累加的那个会话**)。
//
// 单会话时它就是"当前会话",与从前逐字一致;TUI 走这条(单会话)。
// 多会话(页签)请用 StatsFor —— 否则显示的会是"最后一次有消耗的那个会话"的数字。
func (s *Service) Stats() sdk.UsageStats {
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	return s.StatsFor(cur)
}

// StatsFor 指定会话的累计统计(会话不存在/没花过 = 零值 + 全局窗口)。
func (s *Service) StatsFor(sessionID string) sdk.UsageStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c *counter
	if s.buckets != nil {
		c = s.buckets[sessionID]
	}
	st := sdk.UsageStats{Window: s.currentWindowLocked()}
	if c != nil {
		st.PromptTokens = c.prompt
		st.CompletionTokens = c.completion
		st.CachedTokens = c.cached
		st.Requests = c.requests
		st.LastPromptTokens = c.lastPrompt
	}
	return st
}

// Reset 归零**主会话**的累计(切会话时调用;窗口学习保留)。
//
// 为什么是主会话而不是"当前":页签模式下不再有全局的"当前会话",而 TUI 单会话切走时
// 期望新会话从零累计 —— 语义上对应主会话桶。要清别的会话请用 ResetFor。
func (s *Service) Reset() { s.ResetFor("") }

// ResetFor 归零指定会话的累计(窗口学习保留)。
func (s *Service) ResetFor(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buckets != nil {
		delete(s.buckets, sessionID)
		if s.cur == sessionID {
			s.cur = ""
		}
	}
}
