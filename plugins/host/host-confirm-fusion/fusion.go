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
	"log/slog"
	"sort"
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

// namedQuestionnaireer 渠道名 + 整卷呈现者。
type namedQuestionnaireer struct {
	name string
	p    sdk.QuestionnairePresenter
}

// AskQuestionnaire sdk.QuestionnaireService:整卷提问(一次问完 N 题)。
//
// 语义:优先广播给**实现 QuestionnairePresenter 的渠道**(首卷胜出,其余撤销);
// 没有任何渠道支持整卷 ⇒ 逐题 Ask 降级(ACP/第三方 UI 插件走这条,功能不丢,
// 只是没有 tab/确认页;每题的"首答生效"与事件面与单题完全一致)。
// 事件面:整卷路径只发 questionnaire/requested ↔ resolved,**不**为每题补发 question/*;
// 逐题降级路径由 Ask 自己发每题一对 question/*(见 sdk/interaction.go 常量注释)。
func (f *Fusion) AskQuestionnaire(ctx context.Context, qn sdk.Questionnaire) (sdk.QuestionnaireAnswer, error) {
	qn = sdk.NormalizeQuestionnaire(qn)
	f.emit(sdk.EventQuestionnaireRequested, &sdk.QuestionnaireEvent{Questionnaire: qn})
	ans, channel, err := f.askQuestionnaireInner(ctx, qn)
	f.emit(sdk.EventQuestionnaireResolved, &sdk.QuestionnaireEvent{
		Questionnaire: qn, Answer: ans, Resolved: true, Channel: channel, Err: errStr(err),
	})
	return ans, err
}

// askQuestionnaireInner 整卷广播与竞速(事件包裹在 AskQuestionnaire);返回胜出渠道名。
func (f *Fusion) askQuestionnaireInner(ctx context.Context, qn sdk.Questionnaire) (sdk.QuestionnaireAnswer, string, error) {
	f.mu.Lock()
	caps := make([]namedQuestionnaireer, 0, len(f.questioners))
	for name, p := range f.questioners {
		if qp, ok := p.(sdk.QuestionnairePresenter); ok {
			caps = append(caps, namedQuestionnaireer{name: name, p: qp})
		}
	}
	f.mu.Unlock()
	if len(caps) == 0 {
		// 无整卷渠道:逐题降级(已答部分在失败/取消时保留,由 sdk 保证)
		ans, err := sdk.AskQuestionnaireSequential(ctx, f, qn)
		return ans, "", err
	}
	// 按渠道名排序:map 遍历无序,排序让"同权重时谁先呈现"在多端测试里可预测
	sort.Slice(caps, func(i, j int) bool { return caps[i].name < caps[j].name })

	type active struct {
		name   string
		ch     <-chan sdk.QuestionnaireAnswer
		cancel func()
	}
	var cases []active
	for _, np := range caps {
		ch, cancel, err := np.p.PresentQuestionnaire(ctx, qn)
		if err != nil || ch == nil {
			continue // 该渠道整卷呈现不可用:跳过,由其它渠道接手
		}
		cases = append(cases, active{name: np.name, ch: ch, cancel: cancel})
	}
	defer func() {
		for _, c := range cases {
			c.cancel()
		}
	}()
	if len(cases) == 0 {
		return sdk.QuestionnaireAnswer{}, "", fmt.Errorf("questionnaire: 全部渠道呈现失败")
	}
	if len(cases) == 1 {
		select {
		case a := <-cases[0].ch:
			return a, cases[0].name, nil
		case <-ctx.Done():
			return sdk.QuestionnaireAnswer{}, "", ctx.Err()
		}
	}
	type won struct {
		ans  sdk.QuestionnaireAnswer
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
		return sdk.QuestionnaireAnswer{}, "", ctx.Err()
	}
}

// Register 注册渠道呈现者(Disposer 幂等撤销)。
func (f *Fusion) Register(channel string, p sdk.ConfirmPresenter) sdk.Disposer {
	f.mu.Lock()
	f.registerSeq++
	key := channel
	if _, dup := f.presenters[key]; dup {
		// 同名渠道后注册覆盖(热重载/重复装配安全):记 WARN 便于定位注册冲突
		logger := slog.Default()
		if f.c != nil && f.c.Logger() != nil {
			logger = f.c.Logger()
		}
		logger.Warn("host-confirm-fusion: 渠道重复注册,覆盖既有呈现者", "channel", key)
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
		// 用户按停止与“等超时”对前端是两件事(见 sdk/interaction.go 的 Canceled)。
		Canceled: ctx.Err() != nil,
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
