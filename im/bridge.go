// 桥核心:入站管线(gate → 去重 → 交互归属 → 命令/回合)+ 输出聚合 + IM ConfirmService。
// 与 ui-web-app 对齐的宿主消费面:回合经 ctx.agentLoop.Run 注入,输出经 SessionLog 回放聚合;
// 审批经 sdk.ConfirmService(本桥实现,供 policy-guard 注入);回合互斥/忙闲由桥内串行化保证。
package im

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Bridge IM 桥:入站消息处理 + 回合驱动 + 确认服务。并发安全(回合串行;多 transport goroutine 可并发 HandleInbound)。
type Bridge struct {
	c        sdk.Ctx
	loop     sdk.AgentLoop  // ctx.agentLoop(必需)
	sessions sdk.SessionLog // ctx.sessions(必需;输出聚合)
	tr       Transport
	acc      *Access
	opt      Options
	turn     sdk.TurnControl // ctx.turnControl(可选;/stop 取消依赖)

	cwd  sdk.CwdSessions // ctx.cwdSessions(P1 会话绑定;可选——未装配会话命令降级)
	llm  sdk.LLMService  // ctx.llm(/status 模型名;可选)
	jobs sdk.JobService // ctx.jobs(/bg 后台任务;可选——未装配命令降级)
	bind *bindStore      // chat→宿主会话映射(opt.SessionBindPath;nil = 无绑定能力)

	mu       sync.Mutex
	busy     bool
	curRoute Route               // 当前回合归属会话(审批确认推送目标)
	cmds     sdk.CommandRegistry // ctx.commands(可选;RegisterCommands 注入)

	qMu     sync.Mutex
	queued  []queuedInbound // P1 忙时队列(全局 FIFO 单槽;回合完成自动续跑)

	confirmMu sync.Mutex
	pending   map[string]*confirmWait // route.Key() → 待回答确认

	askMu    sync.Mutex
	qPending map[string]*questionWait // route.Key() → 待回答提问(P3 语义交互)

	dedupMu sync.Mutex
	dedup   map[string]time.Time // route+msgid → 首次 seen(窗口裁剪)
}

// queuedInbound 一条忙时排队的入站(回合结束后按序续跑)。
type queuedInbound struct {
	route Route
	text  string
	atts  []sdk.Attachment
}

// confirmWait 一条待回答的确认(policy 侧 Confirm 阻塞等待;用户消息经 answerPending 回填)。
type confirmWait struct {
	ch chan bool
}

// questionWait 一条待回答的结构化提问(工具侧 Ask 阻塞等待;用户消息经 answerQuestion 回填)。
type questionWait struct {
	q  sdk.Question
	ch chan sdk.QuestionAnswer
}

const dedupWindow = 5 * time.Minute
const dedupCap = 1024

// New 构造桥。loop/sessions/tr 为必需依赖(插件壳装配时注入)。
func New(c sdk.Ctx, loop sdk.AgentLoop, sessions sdk.SessionLog, tr Transport, opt Options) *Bridge {
	o := defaultOptions()
	if opt.Mode != "" {
		o.Mode = opt.Mode
	}
	if opt.PairingTTL > 0 {
		o.PairingTTL = opt.PairingTTL
	}
	if opt.BusyReply != "" {
		o.BusyReply = opt.BusyReply
	}
	if opt.AsyncAfter > 0 {
		o.AsyncAfter = opt.AsyncAfter
	}
	if opt.AsyncNotice != "" {
		o.AsyncNotice = opt.AsyncNotice
	}
	if opt.PairingReply != nil {
		o.PairingReply = opt.PairingReply
	}
	if opt.Allow != nil {
		o.Allow = opt.Allow
	}
	b := &Bridge{
		c: c, loop: loop, sessions: sessions, tr: tr,
		acc:     NewAccess(o.Mode, o.Allow, o.PairingTTL),
		opt:     o,
		pending:  make(map[string]*confirmWait),
		qPending: make(map[string]*questionWait),
		dedup:    make(map[string]time.Time),
		bind:    newBindStore(o.SessionBindPath),
	}
	b.injectOptionalServices(c) // 可选:ctx.cwdSessions/ctx.llm(P1 会话绑定/模型名)
	return b
}

// injectOptionalServices 尝试注入可选宿主服务(P1 会话绑定/cwd;缺失静默跳过——
// 相应命令面降级提示)。仅当 New 收到真实 Ctx(插件壳传入)时执行。
func (b *Bridge) injectOptionalServices(c sdk.Ctx) {
	if c == nil {
		return
	}
	var cwd sdk.CwdSessions
	if err := c.Inject("ctx.cwdSessions", &cwd); err == nil {
		b.cwd = cwd
	}
	var lls sdk.LLMService
	if err := c.Inject("ctx.llm", &lls); err == nil {
		b.llm = lls
	}
	var jb sdk.JobService
	if err := c.Inject("ctx.jobs", &jb); err == nil {
		b.jobs = jb
	}
}

// SetTurnControl 注入回合控制(host 装配 ctx.turnControl;未装配 /stop 为 no-op)。
func (b *Bridge) SetTurnControl(t sdk.TurnControl) { b.turn = t }

// Access 暴露访问控制(主机命令/测试调整用)。
func (b *Bridge) Access() *Access { return b.acc }

// HandleInbound 处理一条已解析入站消息(transport 逐条调用;可并发)。
func (b *Bridge) HandleInbound(ctx context.Context, in Inbound) error {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil
	}
	r := in.Route
	if r.ChatID == "" {
		r.ChatID = r.UserID
	}
	if r.UserID == "" {
		return nil
	}

	// 1. 访问控制(gate;drop 静默,防枚举)
	res, code := b.acc.Gate(r.SenderKey())
	switch res {
	case gateDrop:
		return nil
	case gatePair:
		msg := ""
		if code != "" && b.opt.PairingReply != nil {
			msg = b.opt.PairingReply(code)
		}
		if msg != "" {
			return b.sendText(ctx, r, msg)
		}
		return nil
	}

	// 2. 去重(通道消息 id;5min 窗口)
	if in.MsgID != "" && !b.markSeen(r, in.MsgID) {
		return nil
	}

	// 3. 交互归属判定(回合中用户回答确认/提问;先于 busy 与回合,防打断)
	if b.answerPending(ctx, r, text) {
		return nil
	}
	if b.answerQuestion(ctx, r, text) {
		return nil
	}

	// 4. 命令面(随时可执行——/stop 需在回合中生效;普通命令输出回 IM)
	if strings.HasPrefix(text, "/") {
		return b.dispatchCommand(ctx, r, text)
	}

	// 5. 回合驱动(P1 busy queue:忙时入队 FIFO 单槽,完成后自动续跑;队列满回"忙")
	b.mu.Lock()
	busy := b.busy
	b.mu.Unlock()
	if busy {
		if b.enqueue(r, text, in.Attachments) {
			return b.sendText(ctx, r, "⏳ 正在处理上一条消息,你已加入队列(完成后自动处理;可 /stop 取消)。")
		}
		return b.sendText(ctx, r, b.opt.BusyReply)
	}
	return b.runTurn(ctx, r, text, in.Attachments)
}

// Present sdk.ConfirmPresenter(P3 融合):把审批推给当前回合归属用户(文字 y/n),
// 返回应答通道;应答经用户消息 answerPending 回填。无活动回合显式报错。
func (b *Bridge) Present(ctx context.Context, prompt string) (<-chan bool, func(), error) {
	b.mu.Lock()
	route := b.curRoute
	b.mu.Unlock()
	if route.UserID == "" {
		return nil, nil, fmt.Errorf("im: 无活动回合归属会话,无法确认")
	}
	w := &confirmWait{ch: make(chan bool, 1)}
	b.confirmMu.Lock()
	b.pending[route.Key()] = w
	b.confirmMu.Unlock()
	// 推送后 pending 等待;应答(answerPending 删除/回填)或超时(Fusion cancel)后清理
	if err := b.sendText(ctx, route, "🔐 需要确认: "+prompt+"\n回复 y 批准 / n 拒绝(约 2 分钟无回复自动拒绝)"); err != nil {
		b.confirmMu.Lock()
		delete(b.pending, route.Key())
		b.confirmMu.Unlock()
		return nil, nil, err
	}
	cancel := func() { // 幂等:应答已回填后删除无副作用
		b.confirmMu.Lock()
		delete(b.pending, route.Key())
		b.confirmMu.Unlock()
	}
	return w.ch, cancel, nil
}

// Confirm sdk.ConfirmService:Present + 等待应答;ctx 取消/超时安全默认拒绝。
func (b *Bridge) Confirm(ctx context.Context, prompt string) (bool, error) {
	b.mu.Lock()
	route := b.curRoute
	b.mu.Unlock()
	ch, cancel, err := b.Present(ctx, prompt)
	if err != nil {
		return false, err
	}
	defer cancel()
	select {
	case ok := <-ch:
		return ok, nil
	case <-ctx.Done():
		_ = b.sendText(context.Background(), route, "⏰ 确认等待超时,已按拒绝处理(如需执行请重新发一条消息触发)。")
		return false, ctx.Err()
	}
}

// answerPending 用户消息是否在回答某个待确认(consumed=true 表示已消费,勿当普通消息)。
func (b *Bridge) answerPending(ctx context.Context, r Route, text string) bool {
	b.confirmMu.Lock()
	w, ok := b.pending[r.Key()]
	b.confirmMu.Unlock()
	if !ok {
		return false
	}
	ok2, known := parseConfirmReply(text)
	if !known {
		_ = b.sendText(ctx, r, "请回复 y 批准 / n 拒绝")
		return true
	}
	b.confirmMu.Lock()
	delete(b.pending, r.Key())
	b.confirmMu.Unlock()
	// 即时回执:让用户确认回填已生效(真机反馈“y 后一直操作中”→ 需区分
	// 回填未达 vs 回合仍在执行;回执先于回合恢复发出,顺序合理)。
	if ok2 {
		_ = b.sendText(context.Background(), r, "✅ 已批准,继续执行…")
	} else {
		_ = b.sendText(context.Background(), r, "❌ 已拒绝,操作未执行。")
	}
	select {
	case w.ch <- ok2:
	default: // 已超时(Confirm 侧已返回);不回填
	}
	return true
}

// PresentQuestion sdk.QuestionPresenter(P3 语义交互):把结构化提问推给当前回合归属用户
// (编号选项 + 自由文本提示),返回作答通道;cancel 幂等清理。
func (b *Bridge) PresentQuestion(ctx context.Context, q sdk.Question) (<-chan sdk.QuestionAnswer, func(), error) {
	b.mu.Lock()
	route := b.curRoute
	b.mu.Unlock()
	if route.UserID == "" {
		return nil, nil, fmt.Errorf("im: 无活动回合归属会话,无法提问(未装配提问通道)")
	}
	w := &questionWait{q: q, ch: make(chan sdk.QuestionAnswer, 1)}
	b.askMu.Lock()
	b.qPending[route.Key()] = w
	b.askMu.Unlock()
	cancel := func() {
		b.askMu.Lock()
		delete(b.qPending, route.Key())
		b.askMu.Unlock()
	}
	if err := b.sendText(ctx, route, questionText(q)); err != nil {
		cancel()
		return nil, nil, err
	}
	return w.ch, cancel, nil
}

// questionText 提问推送文本(编号选项;自由文本提示;多选说明)。
func questionText(q sdk.Question) string {
	var sb strings.Builder
	sb.WriteString("❓ " + q.Prompt)
	for i, o := range q.Options {
		desc := o.Desc
		if desc == "" {
			desc = o.Value
		}
		sb.WriteString(fmt.Sprintf("\n  %d) %s", i+1, desc))
	}
	switch {
	case len(q.Options) > 0 && q.Multiple:
		sb.WriteString("\n回复编号(多选可用逗号分隔,如 1,3)")
	case len(q.Options) > 0:
		sb.WriteString("\n回复编号选择")
	}
	if q.FreeText || len(q.Options) == 0 {
		sb.WriteString("\n(也可直接回复内容作答)")
	}
	return sb.String()
}

// answerQuestion 用户消息是否为某待答提问的作答(consumed=true 表示已消费)。
// 解析:编号(1-based)/选项值精确匹配(多选取并集);纯文本提问或无匹配且允许自由文本 → Text。
func (b *Bridge) answerQuestion(ctx context.Context, r Route, text string) bool {
	b.askMu.Lock()
	w, ok := b.qPending[r.Key()]
	b.askMu.Unlock()
	if !ok {
		return false
	}
	ans, matched := parseQuestionAnswer(w.q, text)
	if !matched {
		_ = b.sendText(ctx, r, "请按提示回复编号"+func() string {
			if w.q.Multiple {
				return "(多选可用逗号分隔)"
			}
			return ""
		}())
		return true // 消费该条,防误入回合
	}
	b.askMu.Lock()
	delete(b.qPending, r.Key())
	b.askMu.Unlock()
	select {
	case w.ch <- ans:
	default: // 已超时(调用侧已返回)
	}
	return true
}

// parseQuestionAnswer 解析用户回答:返回作答与是否可用。
// 选项匹配优先(编号 / Value 精确 / Desc 精确);无匹配时若允许自由文本则整段作为 Text。
func parseQuestionAnswer(q sdk.Question, text string) (sdk.QuestionAnswer, bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return sdk.QuestionAnswer{}, false
	}
	if len(q.Options) == 0 { // 纯自由文本提问
		if q.FreeText || true { // 无选项必为文本作答
			return sdk.QuestionAnswer{Text: t}, true
		}
	}
	parts := strings.FieldsFunc(t, func(r rune) bool {
		return r == ',' || r == '，' || r == ' ' || r == '、' || r == ';' || r == '；' || r == '\n' || r == '\t'
	})
	var values []string
	seen := map[string]bool{}
	matchedAll := true
	for _, p := range parts {
		matched := ""
		if n, err := strconv.Atoi(p); err == nil {
			if n >= 1 && n <= len(q.Options) {
				matched = q.Options[n-1].Value
			}
		} else {
			for _, o := range q.Options {
				if o.Value == p || (o.Desc != "" && o.Desc == p) {
					matched = o.Value
					break
				}
			}
		}
		if matched == "" {
			matchedAll = false
			break
		}
		if !seen[matched] {
			seen[matched] = true
			values = append(values, matched)
		}
	}
	if matchedAll && len(values) > 0 {
		if !q.Multiple && len(values) > 1 {
			return sdk.QuestionAnswer{}, false // 单选却回多个:提示重答
		}
		return sdk.QuestionAnswer{Values: values}, true
	}
	if q.FreeText { // 允许自由文本:整段作答
		return sdk.QuestionAnswer{Text: t}, true
	}
	return sdk.QuestionAnswer{}, false
}

// enqueue 忙时入队(全局 FIFO 单槽)。成功返回 true;队列已满返回 false(回"忙")。
func (b *Bridge) enqueue(r Route, text string, atts []sdk.Attachment) bool {
	b.qMu.Lock()
	defer b.qMu.Unlock()
	if len(b.queued) >= 1 {
		return false
	}
	b.queued = append(b.queued, queuedInbound{route: r, text: text, atts: atts})
	return true
}

// dequeue 取队头(锁内调用方自持 qMu;无元素返回 nil)。
func (b *Bridge) dequeue() *queuedInbound {
	if len(b.queued) == 0 {
		return nil
	}
	q := b.queued[0]
	b.queued = b.queued[1:]
	return &q
}

// bindSession 回合前把宿主当前会话切到该 chat 的绑定会话(P1;幂等)。
// 绑定 id 已被宿主删除(磁盘枚举不见)→ 解绑回主会话,防 Open 误新建空会话;
// 当前已在该会话但文件被外部删除同样自愈解绑(每回合一次磁盘枚举,IM 低频可接受)。
func (b *Bridge) bindSession(r Route) {
	if b.cwd == nil || b.bind == nil {
		return // 未装配/无绑定能力:跟随宿主当前会话(默认行为)
	}
	id := b.bind.Get(r.Key())
	if id == "" {
		return
	}
	exists := false
	for _, s := range b.cwd.Sessions() {
		if s.ID == id {
			exists = true
			break
		}
	}
	if !exists {
		b.bind.Set(r.Key(), "") // 会话已删 → 解绑回主会话
		return
	}
	if b.cwd.CurrentSession() != id {
		_ = b.cwd.Open(id)
	}
}

// releaseSlot 释放回合槽(/stop 后置、回合完成、/bg 完成):busy 复位 → 续跑队列
// 下一条(busy 槽直接交接,防并发双开)。typing 停止由各调用路径自行处理。
func (b *Bridge) releaseSlot() {
	b.mu.Lock()
	b.busy = false
	b.curRoute = Route{}
	b.qMu.Lock()
	next := b.dequeue()
	b.qMu.Unlock()
	if next != nil {
		b.busy = true
		b.curRoute = next.route
	}
	b.mu.Unlock()
	if next != nil {
		go func() {
			_ = b.runTurn(context.Background(), next.route, next.text, next.atts)
		}()
	}
}

// runTurn 回合驱动:绑定会话 → 记录回合起点 seq → agentLoop.Run → 聚合回合内
// 新增 assistant 最终文本回推;结束路径(成功/错误)统一停 typing 并续跑队列下一条。
// 忙闲由调用方(HandleInbound/enqueue 续跑)保证——runTurn 自身不再查 busy。
func (b *Bridge) runTurn(ctx context.Context, r Route, text string, atts []sdk.Attachment) error {
	b.bindSession(r) // P1:按 chat 绑定切到宿主会话(未绑定/无能力 = 跟随当前)
	b.mu.Lock()
	b.busy = true
	b.curRoute = r
	b.mu.Unlock()
	// typing:回合进行中持续“正在输入”(Transport 实现 TypingAware 时;best-effort,
	// 用户凭此判断仍在工作 vs 断联——真机反馈)。结束路径(成功/错误)统一停止。
	var ta TypingAware
	if t, ok := b.tr.(TypingAware); ok {
		ta = t
	}
	if ta != nil {
		_ = ta.ShowTyping(context.Background(), r)
	}
	defer func() {
		if ta != nil {
			_ = ta.StopTyping(context.Background(), r)
		}
	}()
	defer b.releaseSlot() // 结束路径统一:释放 busy 槽 + 续跑队列

	// 回合执行(§7.5 P2):AsyncAfter 阈值内未完成 → 回“处理中”并转入后台,
	// 完成后仍自动回推(QQ 被动窗口过期由 transport 门控自动转主动配额/滞留)。
	return b.runExecution(ctx, r, text, atts)
}

// runExecution 同步/后台执行回合:完成或(启用时)超阈值转后台通知。回合槽(busy)
// 保持占用至完成(agent-loop 单飞写会话,防并发回合错乱);队列消息随后续 drain。
func (b *Bridge) runExecution(ctx context.Context, r Route, text string, atts []sdk.Attachment) error {
	seq0 := b.lastSeq()
	ch := make(chan error, 1)
	go func() {
		ch <- b.executeTurn(ctx, r, text, atts, seq0)
	}()
	if b.opt.AsyncAfter <= 0 {
		return <-ch
	}
	select {
	case err := <-ch:
		return err
	case <-time.After(b.opt.AsyncAfter):
		_ = b.sendText(context.Background(), r, b.opt.AsyncNotice)
		return <-ch // 转入后台:继续等待完成(结果回推仍发生)
	}
}

// executeTurn 执行一次回合:loop 注入(附件优先)→ 错误回推/聚合最终文本回推。
func (b *Bridge) executeTurn(ctx context.Context, r Route, text string, atts []sdk.Attachment, seq0 uint64) error {
	var err error
	// 媒体附件(如有):经 AttachmentInput 注入回合(图片视觉/文件路径引用);未实现回落 Run
	if len(atts) > 0 {
		if al, ok := b.loop.(sdk.AttachmentInput); ok {
			err = al.RunWithAttachments(ctx, text, atts)
		} else {
			err = b.loop.Run(ctx, text) // 文本已含媒体描述占位(ExtractText),降级不丢内容
		}
	} else {
		err = b.loop.Run(ctx, text)
	}
	if err != nil {
		var msg string
		if errors.Is(err, context.Canceled) {
			msg = "⏹ 已取消(/stop)"
		} else {
			msg = "❌ 处理失败: " + err.Error()
		}
		_ = b.sendText(context.Background(), r, msg)
		return err
	}
	texts := b.assistantTextsAfter(seq0)
	if len(texts) == 0 {
		return b.sendText(context.Background(), r, "✅ 完成")
	}
	// 只回推最终 assistant 文本(中间过程行留 P1 可选 step push)
	return b.sendText(context.Background(), r, texts[len(texts)-1])
}

// dispatchCommand 斜杠命令经注入的 ctx.commands 分发(宿主命令 /jobs 等与桥命令 /stop、/im)。
func (b *Bridge) dispatchCommand(ctx context.Context, r Route, line string) error {
	b.mu.Lock()
	cmds := b.cmds
	b.mu.Unlock()
	// 桥自带命令优先(回合中 /stop 必须可用,即使宿主无 ctx.commands)
	name, args := splitCommand(line)
	switch name {
	case "stop":
		if b.turn != nil {
			b.turn.Cancel()
		}
		return b.sendText(ctx, r, "⏹ 已请求停止当前回合。")
	case "im":
		return b.sendText(ctx, r, b.imCmd(ctx, args))
	case "bg":
		return b.sendText(ctx, r, b.bgCmd(r, args))
	case "new", "status", "sessionlist", "session", "history":
		// P1 会话绑定命令面(IM 通道专属,带 route 上下文;不注册全局——
		// 宿主 /session 等命令归 host-internal-commands,IM 绑定语义与其不同)
		return b.sendText(ctx, r, b.sessionCmd(ctx, r, name, args))
	}
	if cmds == nil {
		return b.sendText(ctx, r, "命令不可用: 宿主 ctx.commands 未装配")
	}
	spec, ok := cmds.Get(name)
	if !ok {
		return b.sendText(ctx, r, "未知命令 /"+name+"(可尝试 /help)")
	}
	out, err := spec.Run(args)
	if err != nil {
		return b.sendText(ctx, r, "命令 /"+name+" 失败: "+err.Error())
	}
	return b.sendText(ctx, r, out)
}

// bgCmd /bg <任务>:显式转入后台(P2 §7.5;经 ctx.jobs 托管,回合槽保持占用至完成,
// 防并发双 loop 写会话;完成后聚合输出主动回推)。仅空闲可提交。
func (b *Bridge) bgCmd(r Route, args []string) string {
	if b.jobs == nil {
		return "后台任务服务不可用: 宿主 host-jobs 未装配。"
	}
	task := strings.Join(args, " ")
	if task == "" {
		return "用法: /bg <任务描述> —— 后台执行,完成自动推送结果(可用 /jobs 查看)。"
	}
	b.mu.Lock()
	if b.busy {
		b.mu.Unlock()
		return "⏳ 回合进行中,请稍后再提交(/stop 可取消当前回合)。"
	}
	b.busy = true
	b.curRoute = r
	b.mu.Unlock()
	go b.submitBG(r, task)
	return "✅ 已转入后台执行;完成自动推送结果(可用 /jobs 查看任务)。"
}

// submitBG 后台任务体:host-jobs 托管;回合槽由 job 完成时释放(防并发双 loop)。
func (b *Bridge) submitBG(r Route, task string) {
	b.bindSession(r)
	seq0 := b.lastSeq()
	if _, err := b.jobs.Run(func(c context.Context) (any, error) {
		defer b.releaseSlot() // job 真正完成才释放 busy 槽 + 续跑队列
		err := b.loop.Run(c, task)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				_ = b.sendText(context.Background(), r, "❌ 后台任务失败: "+err.Error())
			}
			return nil, err
		}
		texts := b.assistantTextsAfter(seq0)
		out := "✅ 完成(无文本输出)"
		if len(texts) > 0 {
			out = texts[len(texts)-1]
		}
		_ = b.sendText(context.Background(), r, "🔄 [后台任务完成]\n"+out)
		return out, nil
	}); err != nil {
		b.releaseSlot() // 提交失败:立即恢复
		_ = b.sendText(context.Background(), r, "❌ 后台任务提交失败: "+err.Error())
	}
}

// imCmd /im 子命令(状态/配对;allow/revoke 由插件壳或主机直调 Access)。
func (b *Bridge) imCmd(ctx context.Context, args []string) string {
	if len(args) == 0 {
		return "用法: /im status|pair <配对码>|list"
	}
	switch args[0] {
	case "status":
		b.mu.Lock()
		busy := b.busy
		b.mu.Unlock()
		state := "idle"
		if busy {
			state = "busy"
		}
		return fmt.Sprintf("im(%s) 模式=%s 已授权=%d 状态=%s", b.tr.Name(), b.acc.Mode(), len(b.acc.List()), state)
	case "pair":
		if len(args) < 2 {
			return "用法: /im pair <配对码>"
		}
		if b.acc.ApprovePair(strings.TrimSpace(args[1])) {
			return "✅ 配对成功,用户已授权。"
		}
		return "配对码无效或已过期。"
	case "list":
		return "已授权:\n" + strings.Join(b.acc.List(), "\n")
	default:
		return "用法: /im status|pair <配对码>|list"
	}
}

// sessionCmd P1 会话绑定命令面(IM 通道专属;仅授权用户可达——gate 已在前置裁决)。
// 宿主会话服务未装配时全部降级提示。命令:
//
//	/new            新建宿主会话并绑定本聊天
//	/status         模型 · 当前会话 · 忙闲
//	/sessionlist    枚举当前项目会话(仅元数据)
//	/session <id>   绑定到既有会话;main = 解绑回主会话;无参 = 当前绑定
//	/history [N]    回读当前(绑定)会话最近 N 条对话(默认 10,上限 50)
func (b *Bridge) sessionCmd(_ context.Context, r Route, name string, args []string) string {
	if b.cwd == nil {
		return "会话服务不可用: 宿主 host-cwd-sessions 未装配(该能力需 base bundle)。"
	}
	// 切换类命令(/new /session)在回合进行中拒绝——agent-loop 正写当前会话,
	// 中途切落盘目标会使回合内事件错乱;只读命令(/status /sessionlist /history)可随时用。
	if name == "new" || name == "session" {
		b.mu.Lock()
		busy := b.busy
		b.mu.Unlock()
		if busy {
			return "⏳ 回合进行中,请稍候再切换会话(或 /stop 取消当前回合)。"
		}
	}
	switch name {
	case "new":
		id, err := b.cwd.New()
		if err != nil {
			return "❌ 新建会话失败: " + err.Error()
		}
		b.bind.Set(r.Key(), id)
		return "✅ 已新建会话并绑定本聊天: " + id + "\n(后续消息进入该会话;/sessionlist 查看,主会话不受影响)"
	case "status":
		return b.statusText()
	case "sessionlist":
		return b.sessionListText()
	case "session":
		return b.sessionBindCmd(r, args)
	case "history":
		return b.historyText(args)
	}
	return ""
}

// statusText /status:模型 · 会话 · 忙闲(权限 = 渠道访问策略,列表类命令仅授权用户可见)。
func (b *Bridge) statusText() string {
	model := "未设置"
	if b.llm != nil && b.llm.Model() != "" {
		model = b.llm.Model()
	}
	sid := b.cwd.CurrentSession()
	label := "主会话"
	if sid != "" {
		if n := b.cwd.SessionName(); n != "" {
			label = sid + "(" + n + ")"
		} else {
			label = sid
		}
	}
	b.mu.Lock()
	busy := b.busy
	b.mu.Unlock()
	state := "空闲"
	if busy {
		state = "忙碌"
	}
	return fmt.Sprintf("模型: %s\n会话: %s\n状态: %s(已授权 %d)", model, label, state, len(b.acc.List()))
}

// sessionListText /sessionlist:枚举当前项目会话(倒序 mtime;当前会话标记 *)。
func (b *Bridge) sessionListText() string {
	infos := b.cwd.Sessions()
	if len(infos) == 0 {
		return "当前项目暂无会话。"
	}
	cur := b.cwd.CurrentSession()
	var sb strings.Builder
	sb.WriteString("会话列表(当前项目):\n")
	for _, s := range infos {
		mark := " "
		if s.ID == cur {
			mark = "*"
		}
		name := s.ID
		if name == "" {
			name = "主会话"
		}
		if s.Name != "" {
			name += "(" + s.Name + ")"
		}
		preview := ""
		if s.Preview != "" {
			preview = " · " + truncateRunes(s.Preview, 36)
		}
		sb.WriteString(fmt.Sprintf(" %s %s%s\n", mark, name, preview))
	}
	sb.WriteString("绑定: /session <id>;新建: /new;回主: /session main")
	return strings.TrimRight(sb.String(), "\n")
}

// sessionBindCmd /session:绑定/解绑/查询当前绑定。
func (b *Bridge) sessionBindCmd(r Route, args []string) string {
	key := r.Key()
	if len(args) == 0 {
		cur := b.bind.Get(key)
		label := "主会话(未绑定)"
		if cur != "" {
			label = cur
		}
		return "当前绑定: " + label + "\n用法: /session <id>|main(/sessionlist 查看可用 id)"
	}
	arg := args[0]
	if arg == "main" || arg == "0" {
		if err := b.cwd.Open(""); err != nil {
			return "❌ 切回主会话失败: " + err.Error()
		}
		b.bind.Set(key, "")
		return "✅ 已解绑,回到主会话。"
	}
	for _, s := range b.cwd.Sessions() {
		if s.ID == arg {
			if err := b.cwd.Open(arg); err != nil {
				return "❌ 切换会话失败: " + err.Error()
			}
			b.bind.Set(key, arg)
			return "✅ 已绑定会话: " + arg + "\n(/history 回读,后续消息进入该会话)"
		}
	}
	return "会话不存在: " + arg + "(可 /sessionlist 查看)"
}

// historyText /history [N]:回读当前(绑定)会话最近 N 条 user/assistant 文本。
func (b *Bridge) historyText(args []string) string {
	n := 10
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
			n = v
		}
	}
	if n > 50 {
		n = 50
	}
	var lines []string
	for _, ev := range b.sessions.Replay() {
		switch ev.Kind {
		case sdk.EventUserMessage:
			if u, ok := ev.Payload.(sdk.UserMessage); ok && u.Content != "" {
				lines = append(lines, "❯ "+u.Content)
			}
		case sdk.EventAssistantMessage:
			if text := assistantContent(ev.Payload); text != "" {
				lines = append(lines, "🤖 "+text)
			}
		}
	}
	if len(lines) == 0 {
		return "该会话暂无历史。"
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n\n")
}

// truncateRunes 按 rune 截断(超长追加省略号)。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// RegisterCommands 向 ctx.commands 注册桥命令(/stop /im;TUI/Web/IM 全端可见)。
// 返回 Disposer 随插件卸载撤销;同名冲突由宿主拒绝。
func (b *Bridge) RegisterCommands(cmds sdk.CommandRegistry) (sdk.Disposer, error) {
	ds := make([]sdk.Disposer, 0, 2)
	b.mu.Lock()
	b.cmds = cmds
	b.mu.Unlock()
	d1, err := cmds.Register(sdk.CommandSpec{
		Name:  "stop",
		Usage: "/stop",
		Desc:  "取消运行中的回合(IM 远程控制/Web/TUI 共用)",
		Run: func(_ []string) (string, error) {
			if b.turn != nil {
				b.turn.Cancel()
			}
			return "⏹ 已请求停止当前回合。", nil
		},
	})
	if err != nil {
		return nil, err
	}
	ds = append(ds, d1)
	d2, err := cmds.Register(sdk.CommandSpec{
		Name:  "im",
		Usage: "/im status|pair <配对码>|list",
		Desc:  "IM 远程控制状态/配对(授权新用户)",
		Run: func(args []string) (string, error) {
			return b.imCmd(context.Background(), args), nil
		},
		// 二级选项:status/list 直接执行;pair 再输入配对码(自由参数)
		Args: []sdk.ArgLevel{
			{Options: func([]string) []sdk.Option {
				return []sdk.Option{
					{Value: "status", Desc: "通道状态(模式/已授权/忙闲)"},
					{Value: "list", Desc: "已授权用户列表"},
					{Value: "pair", Desc: "用配对码授权新用户"},
				}
			}},
			{FreeArgs: func(picked []string) []string {
				if len(picked) > 0 && picked[0] == "pair" {
					return []string{"配对码"}
				}
				return nil
			}},
		},
	})
	if err != nil {
		d1()
		return nil, err
	}
	ds = append(ds, d2)
	return func() {
		for _, d := range ds {
			d()
		}
	}, nil
}

// —— 内部工具 ——

// sendText 统一出站(空文本跳过)。
func (b *Bridge) sendText(ctx context.Context, r Route, text string) error {
	if text == "" {
		return nil
	}
	return b.tr.SendText(ctx, r, text)
}

// markSeen 去重登记:重复返回 false。
func (b *Bridge) markSeen(r Route, msgID string) bool {
	key := r.Key() + "\x00" + msgID
	now := time.Now()
	b.dedupMu.Lock()
	defer b.dedupMu.Unlock()
	if ts, ok := b.dedup[key]; ok && now.Sub(ts) < dedupWindow {
		return false
	}
	b.dedup[key] = now
	if len(b.dedup) > dedupCap { // 窗口裁剪:超限清理最旧的一半
		for k, ts := range b.dedup {
			if now.Sub(ts) >= dedupWindow || len(b.dedup) <= dedupCap/2 {
				delete(b.dedup, k)
			}
		}
	}
	return true
}

// lastSeq 当前会话日志最后 seq(回合起点游标)。
func (b *Bridge) lastSeq() uint64 {
	evs := b.sessions.Replay()
	if len(evs) == 0 {
		return 0
	}
	return evs[len(evs)-1].Seq
}

// assistantTextsAfter 回放 seq 之后新增的 assistant 完整消息文本(回合内可能有多个 step)。
func (b *Bridge) assistantTextsAfter(after uint64) []string {
	var out []string
	for _, ev := range b.sessions.Replay() {
		if ev.Seq <= after {
			continue
		}
		if ev.Kind != sdk.EventAssistantMessage {
			continue
		}
		if text := assistantContent(ev.Payload); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// assistantContent 从事件载荷解出 assistant 文本(进程内值类型;坏载荷回空)。
func assistantContent(p any) string {
	switch v := p.(type) {
	case sdk.AssistantMessage:
		return v.Content
	case *sdk.AssistantMessage:
		if v != nil {
			return v.Content
		}
	case map[string]any: // 会话日志从磁盘重载(map 形态)兜底
		if c, ok := v["content"].(string); ok {
			return c
		}
	}
	return ""
}

// splitCommand 拆 "/name arg1 arg2"。
func splitCommand(line string) (string, []string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", nil
	}
	return strings.TrimPrefix(fields[0], "/"), fields[1:]
}

// parseConfirmReply 解析确认文字回答:(批准?, 是否可识别)。词表首词匹配。
func parseConfirmReply(text string) (bool, bool) {
	yes := map[string]bool{"y": true, "yes": true, "ok": true, "1": true, "是": true, "好": true, "同意": true, "批准": true, "确认": true, "允许": true}
	no := map[string]bool{"n": true, "no": true, "0": true, "否": true, "不要": true, "拒绝": true, "取消": true, "deny": true, "abort": true}
	first := strings.ToLower(strings.TrimSpace(text))
	first = strings.Trim(first, ".,!?。！？ \t")
	if yes[first] {
		return true, true
	}
	if no[first] {
		return false, true
	}
	return false, false
}

// singleQuestion 单通道提问服务适配(P3;无 fusion 的单 profile 场景):
// 桥自身只实现 sdk.QuestionPresenter,这里提供 sdk.QuestionService(Ask 直连本桥)。
type singleQuestion struct{ b *Bridge }

// NewQuestionService 构造单通道提问服务(供 ui-im-* 插件壳在无 host-confirm-fusion 时
// Provide ctx.question;融合场景由 fusion 统一提供,桥经 RegisterQuestioner 注册呈现)。
func NewQuestionService(b *Bridge) sdk.QuestionService { return singleQuestion{b: b} }

// Ask 呈现提问并等待作答(ctx 取消按失败返回)。
func (s singleQuestion) Ask(ctx context.Context, q sdk.Question) (sdk.QuestionAnswer, error) {
	ch, cancel, err := s.b.PresentQuestion(ctx, q)
	if err != nil {
		return sdk.QuestionAnswer{}, err
	}
	defer cancel()
	select {
	case a := <-ch:
		return a, nil
	case <-ctx.Done():
		return sdk.QuestionAnswer{}, ctx.Err()
	}
}

// RegisterQuestioner 单通道场景无需注册(桥即唯一渠道)。
func (s singleQuestion) RegisterQuestioner(string, sdk.QuestionPresenter) sdk.Disposer {
	return func() {}
}
