// 桥核心:入站管线(gate → 去重 → 交互归属 → 命令/回合)+ 输出聚合 + IM ConfirmService。
// 与 ui-web-app 对齐的宿主消费面:回合经 ctx.agentLoop.Run 注入,输出经 SessionLog 回放聚合;
// 审批经 sdk.ConfirmService(本桥实现,供 policy-guard 注入);回合互斥/忙闲由桥内串行化保证。
package im

import (
	"context"
	"errors"
	"fmt"
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

	mu       sync.Mutex
	busy     bool
	curRoute Route               // 当前回合归属会话(审批确认推送目标)
	cmds     sdk.CommandRegistry // ctx.commands(可选;RegisterCommands 注入)

	confirmMu sync.Mutex
	pending   map[string]*confirmWait // route.Key() → 待回答确认

	dedupMu sync.Mutex
	dedup   map[string]time.Time // route+msgid → 首次 seen(窗口裁剪)
}

// confirmWait 一条待回答的确认(policy 侧 Confirm 阻塞等待;用户消息经 answerPending 回填)。
type confirmWait struct {
	ch chan bool
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
	if opt.PairingReply != nil {
		o.PairingReply = opt.PairingReply
	}
	if opt.Allow != nil {
		o.Allow = opt.Allow
	}
	return &Bridge{
		c: c, loop: loop, sessions: sessions, tr: tr,
		acc:     NewAccess(o.Mode, o.Allow, o.PairingTTL),
		opt:     o,
		pending: make(map[string]*confirmWait),
		dedup:   make(map[string]time.Time),
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

	// 4. 命令面(随时可执行——/stop 需在回合中生效;普通命令输出回 IM)
	if strings.HasPrefix(text, "/") {
		return b.dispatchCommand(ctx, r, text)
	}

	// 5. 回合驱动(busy 串行:忙时回提示,不排队——queue 语义 P1)
	return b.runTurn(ctx, r, text)
}

// Confirm sdk.ConfirmService:把审批推给当前回合归属用户,等其文字回答(y/n)。
// ctx 取消/超时安全默认拒绝(对齐 web/confirm.go 语义)。
func (b *Bridge) Confirm(ctx context.Context, prompt string) (bool, error) {
	b.mu.Lock()
	route := b.curRoute
	b.mu.Unlock()
	if route.UserID == "" {
		return false, fmt.Errorf("im: 无活动回合归属会话,无法确认(未装配确认通道)")
	}
	w := &confirmWait{ch: make(chan bool, 1)}
	b.confirmMu.Lock()
	b.pending[route.Key()] = w
	b.confirmMu.Unlock()
	defer func() {
		b.confirmMu.Lock()
		delete(b.pending, route.Key())
		b.confirmMu.Unlock()
	}()
	if err := b.sendText(ctx, route, "🔐 需要确认: "+prompt+"\n回复 y 批准 / n 拒绝"); err != nil {
		return false, err
	}
	select {
	case ok := <-w.ch:
		return ok, nil
	case <-ctx.Done():
		_ = b.sendText(context.Background(), route, "⏰ 确认等待超时,已按拒绝处理。")
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
	select {
	case w.ch <- ok2:
	default: // 已超时(Confirm 侧已返回);不回填
	}
	return true
}

// runTurn 回合驱动:记录回合起点 seq → agentLoop.Run → 聚合回合内新增 assistant 最终文本回推。
func (b *Bridge) runTurn(ctx context.Context, r Route, text string) error {
	b.mu.Lock()
	if b.busy {
		b.mu.Unlock()
		return b.sendText(ctx, r, b.opt.BusyReply)
	}
	b.busy = true
	b.curRoute = r
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.busy = false
		b.curRoute = Route{}
		b.mu.Unlock()
	}()

	seq0 := b.lastSeq()
	err := b.loop.Run(ctx, text)
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
