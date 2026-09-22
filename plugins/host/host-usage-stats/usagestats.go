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
	return func() { d(); d2() }, nil
}

// Service 实现 sdk.UsageStatsService:会话级累计统计。
type Service struct {
	mu                 sync.Mutex
	windowOverride     int            // data.context_window 显式覆盖(0 = 未配置,按模型解析)
	extraWindows       map[string]int // data.model_windows 配置层覆盖(前缀→窗口)
	learned            map[string]int // 错误驱动学习缓存(模型精确名 → 实测窗口)
	model              string
	prompt, completion int
	cached             int
	requests           int
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
			s.add("", u)
		}
		return
	}
	s.add(ue.Model, ue.Usage)
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

// add 累计一笔 usage 并刷新模型。
func (s *Service) add(model string, u sdk.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if model != "" {
		s.model = model
	}
	s.prompt += u.PromptTokens
	s.completion += u.CompletionTokens
	s.cached += u.CachedTokens
	s.requests++
}

// currentWindow 当前窗口:显式覆盖(context_window)> 按模型解析(见 modelwindows.go)> 0(未知)。
// 0 = 窗口未知:展示层只显示使用量。
func (s *Service) currentWindow() int {
	if s.windowOverride > 0 {
		return s.windowOverride
	}
	return s.windowForModel(s.model)
}

// Stats 当前会话累计统计快照。
func (s *Service) Stats() sdk.UsageStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sdk.UsageStats{
		PromptTokens:     s.prompt,
		CompletionTokens: s.completion,
		CachedTokens:     s.cached,
		Requests:         s.requests,
		Window:           s.currentWindow(),
	}
}

// Reset 归零统计(切换会话时调用;新会话从零累计,模型/窗口保留)。
func (s *Service) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompt, s.completion, s.cached, s.requests = 0, 0, 0, 0
	s.model = ""
}
