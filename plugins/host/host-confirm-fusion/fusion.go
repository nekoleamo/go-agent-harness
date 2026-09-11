// host-confirm-fusion:审批确认融合仲裁(多端融合)。Web/TUI 同进程并存时
// ctx.confirm 提供方冲突(core/ctx 同名拒绝)——本插件统一 Provide ctx.confirm(Fusion
// 实现),各 UI 插件改为经 ctx.confirmFusion.Register 注册呈现者(web 弹层/tui 文字
// y·n/tui 弹层),任一渠道应答即生效(双端同卡同决策)。
// 装配了 Fusion 的 profile(base+web+tui+confirm-fusion)不再互斥;未装配 Fusion 的
// 既有 profile(web-only/tui-only)保持各 UI 自 Provide ctx.confirm(向后兼容)。
package hostconfirmfusion

import (
	"context"
	"fmt"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Fusion ctx.confirm 实现 + 呈现者注册表。并发安全。
type Fusion struct {
	c           sdk.Ctx // 事件广播(可 nil:嵌入/单测)
	mu          sync.Mutex
	presenters  map[string]sdk.ConfirmPresenter  // 确认呈现者(注册顺序无关;应答竞速)
	questioners map[string]sdk.QuestionPresenter // 提问呈现者(P3 语义交互;同源管道)
	registerSeq int
}

// Plugin 实现 host-confirm-fusion。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-confirm-fusion" }

// Start 提供 ctx.confirm(Fusion)+ ctx.confirmFusion。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	f := &Fusion{
		c:           c,
		presenters:  make(map[string]sdk.ConfirmPresenter),
		questioners: make(map[string]sdk.QuestionPresenter),
	}
	if err := c.Provide("ctx.confirm", f); err != nil {
		return nil, err
	}
	if err := c.Provide("ctx.confirmFusion", f); err != nil {
		return nil, err
	}
	// P3 语义交互:同一实例提供结构化提问(Ask + 渠道注册)
	if err := c.Provide("ctx.question", f); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// EmitsEvents 交互事件化可用标志(sdk.InteractionObserver)。
func (f *Fusion) EmitsEvents() bool { return f != nil && f.c != nil }

// emit 广播交互事件(best-effort:无 Ctx/失败忽略,不影响呈现)。
func (f *Fusion) emit(name string, payload any) {
	if f == nil || f.c == nil {
		return
	}
	_, _ = f.c.Emit(context.Background(), name, payload, sdk.Emit)
}

// RegisterQuestioner 注册提问渠道呈现者(sdk.QuestionService;Disposer 幂等撤销)。
func (f *Fusion) RegisterQuestioner(channel string, p sdk.QuestionPresenter) sdk.Disposer {
	f.mu.Lock()
	f.registerSeq++
	f.questioners[channel] = p
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		if f.questioners[channel] == p {
			delete(f.questioners, channel)
		}
		f.mu.Unlock()
	}
}

// QuestionChannels 已注册提问渠道(诊断)。
func (f *Fusion) QuestionChannels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.questioners))
	for k := range f.questioners {
		out = append(out, k)
	}
	return out
}

// Ask sdk.QuestionService:广播提问给全部渠道,首答生效;无渠道显式报错(不静默假答)。
// 事件化:补齐 Question.ID(事件与各端弹层共用同一 id)→ 广播 requested → resolved(带胜出渠道)。
func (f *Fusion) Ask(ctx context.Context, q sdk.Question) (sdk.QuestionAnswer, error) {
	if q.ID == "" {
		q.ID = sdk.NewQuestionID()
	}
	f.emit(sdk.EventQuestionRequested, &sdk.QuestionEvent{Question: q})
	ans, channel, err := f.askInner(ctx, q)
	f.emit(sdk.EventQuestionResolved, &sdk.QuestionEvent{
		Question: q, Answer: ans, Resolved: true, Channel: channel, Err: errStr(err),
	})
	return ans, err
}

// askInner 广播提问与竞速作答(事件包裹在 Ask);返回胜出渠道名(供事件审计)。
func (f *Fusion) askInner(ctx context.Context, q sdk.Question) (sdk.QuestionAnswer, string, error) {
	f.mu.Lock()
	presenters := make([]namedQuestioner, 0, len(f.questioners))
	for name, p := range f.questioners {
		presenters = append(presenters, namedQuestioner{name: name, p: p})
	}
	f.mu.Unlock()
	if len(presenters) == 0 {
		return sdk.QuestionAnswer{}, "", fmt.Errorf("question: 无提问渠道(未注册 UI 呈现者)")
	}
	type active struct {
		name   string
		ch     <-chan sdk.QuestionAnswer
		cancel func()
	}
	var cases []active
	for _, np := range presenters {
		ch, cancel, err := np.p.PresentQuestion(ctx, q)
		if err != nil || ch == nil {
			continue
		}
		cases = append(cases, active{name: np.name, ch: ch, cancel: cancel})
	}
	defer func() {
		for _, c := range cases {
			c.cancel()
		}
	}()
	if len(cases) == 0 {
		return sdk.QuestionAnswer{}, "", fmt.Errorf("question: 全部渠道呈现失败")
	}
	if len(cases) == 1 {
		select {
		case a := <-cases[0].ch:
			return a, cases[0].name, nil
		case <-ctx.Done():
			return sdk.QuestionAnswer{}, "", ctx.Err()
		}
	}
	type won struct {
		ans  sdk.QuestionAnswer
		name string
	}
	merged := make(chan won, 1)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(len(cases))
	for _, c := range cases {
		go func(ac active) {
			defer wg.Done()
			select {
			case a := <-ac.ch:
				select {
				case merged <- won{ans: a, name: ac.name}:
				default:
				}
			case <-done:
			}
		}(c)
	}
	go func() { wg.Wait(); close(merged) }()
	defer close(done)
	select {
	case w := <-merged:
		return w.ans, w.name, nil
	case <-ctx.Done():
		return sdk.QuestionAnswer{}, "", ctx.Err()
	}
}

// namedQuestioner 渠道名 + 呈现者(注册表值拷贝,事件审计用)。
type namedQuestioner struct {
	name string
	p    sdk.QuestionPresenter
}

// Register 注册渠道呈现者(Disposer 幂等撤销)。
func (f *Fusion) Register(channel string, p sdk.ConfirmPresenter) sdk.Disposer {
	f.mu.Lock()
	f.registerSeq++
	key := channel
	if _, dup := f.presenters[key]; dup {
		// 同名渠道后注册覆盖(热重载/重复装配安全)
	}
	f.presenters[key] = p
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		if f.presenters[key] == p {
			delete(f.presenters, key)
		}
		f.mu.Unlock()
	}
}

// Channels 已注册渠道(诊断/状态)。
func (f *Fusion) Channels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.presenters))
	for k := range f.presenters {
		out = append(out, k)
	}
	return out
}

// Confirm sdk.ConfirmService:广播呈现给所有渠道,presenter 竞速应答,首个生效。
// 无已注册渠道显式报错(策略侧应安全拒绝,不静默放行);ctx 取消按拒绝处理。
func (f *Fusion) Confirm(ctx context.Context, prompt string) (bool, error) {
	f.emit(sdk.EventConfirmRequested, &sdk.ConfirmEvent{Prompt: prompt})
	ok, channel, err := f.confirmInner(ctx, prompt)
	f.emit(sdk.EventConfirmResolved, &sdk.ConfirmEvent{
		Prompt: prompt, OK: ok, Resolved: true, Channel: channel, Err: errStr(err),
	})
	return ok, err
}

// errStr 错误转字符串(空错误 = "")。
func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// confirmInner 呈现与竞速应答(事件包裹在 Confirm);返回胜出渠道名(供事件审计)。
func (f *Fusion) confirmInner(ctx context.Context, prompt string) (bool, string, error) {
	f.mu.Lock()
	presenters := make([]namedConfirmer, 0, len(f.presenters))
	for name, p := range f.presenters {
		presenters = append(presenters, namedConfirmer{name: name, p: p})
	}
	f.mu.Unlock()
	if len(presenters) == 0 {
		return false, "", context.Canceled // 无确认通道:按取消(拒绝)处理
	}
	// 每渠道一次 Present(prompt);失败渠道跳过(其 UI 不可用)
	type active struct {
		name   string
		ch     <-chan bool
		cancel func()
	}
	var cases []active
	for _, np := range presenters {
		ch, cancel, err := np.p.Present(ctx, prompt)
		if err != nil || ch == nil {
			continue
		}
		cases = append(cases, active{name: np.name, ch: ch, cancel: cancel})
	}
	defer func() { // 结束路径统一清理各渠道呈现(幂等)
		for _, c := range cases {
			c.cancel()
		}
	}()
	if len(cases) == 0 {
		return false, "", context.Canceled
	}
	// 竞速:任一渠道应答即返回(先答先得;多于一个渠道合并监听)
	if len(cases) == 1 {
		select {
		case ok := <-cases[0].ch:
			return ok, cases[0].name, nil
		case <-ctx.Done():
			return false, "", ctx.Err()
		}
	}
	type won struct {
		ok   bool
		name string
	}
	merged := make(chan won, 1)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(len(cases))
	for _, c := range cases {
		go func(ac active) {
			defer wg.Done()
			select {
			case ok := <-ac.ch:
				select {
				case merged <- won{ok: ok, name: ac.name}:
				default:
				}
			case <-done:
			}
		}(c)
	}
	go func() { wg.Wait(); close(merged) }()
	defer close(done)
	select {
	case w := <-merged:
		return w.ok, w.name, nil
	case <-ctx.Done():
		return false, "", ctx.Err()
	}
}

// namedConfirmer 渠道名 + 呈现者(事件审计用)。
type namedConfirmer struct {
	name string
	p    sdk.ConfirmPresenter
}
