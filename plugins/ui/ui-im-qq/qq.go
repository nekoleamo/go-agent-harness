// Package uimqq 提供 ui-im-qq 插件(IM 远程控制线 P0-2b-QQ):QQ 官方 Bot v2(api-v2)
// 接入 im.Bridge——transport(qqbot.Gateway WS 收事件 → 缓存被动回复 msg_id → im.HandleInbound;
// SendText 带 msg_id/msg_seq 被动回复,单聊/群分派)+ 装配(注入 loop/sessions → im.New →
// Provide ctx.confirm + 注册 /stop、/im(桥)+ /qq login|status(通道命令))。
// 凭证入 $GAH_HOME/config/qqbot.yaml(0600;AppID/AppSecret,同 ilink 型)。
// 安全:默认 pairing(配对码经 /im pair 授权);allowlist 经 store 持久。
// 平台差异:群与单聊同一用户是两个 openid(单聊 user_openid / 群 member_openid),
// 访问控制按各自 SenderKey 授权(平台规则,群成员需另行 pair)。呈现层(markdown/键盘)留 T3/T4。
package uimqq

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/im"
	"github.com/nekoleamo/go-agent-harness/qqbot"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 ui-im-qq。Requires ctx.agentLoop/ctx.sessions(注入);提供 ctx.confirm,
// 与 tui/web/im-wechat profile 互斥(ctx.confirm 唯一,profile 层保证)。
type Plugin struct{}

func (p *Plugin) Name() string { return "ui-im-qq" }

const channelName = "qq"

// 出站策略:QQ 单条文本/markdown 上限 4000 字符;被动回复受 5min msg_id 窗口与 msg_seq 幂等约束;
// 分块间小间隔克制气泡刷屏(qps 频控)。预算层抽象(P1 出站预算,见 §6.1)未实施前本包自持。
const (
	qqChunkLimit   = 4000                   // 单条文本/markdown 上限(字符)
	qqChunkGap     = 300 * time.Millisecond // 分块间隔
	qqMaxChunks    = 20                     // 单回合最多分块数(超出截断 + 提示)
	replyWindow    = 5 * time.Minute        // msg_id 被动回复有效期(官方)
	typingInterval = 30 * time.Second       // input_notify 状态非持续,~30s 刷新保持(≤ input_second 60)
)

// replyCtx 会话被动回复上下文(msg_id + 窗口起点)。
type replyCtx struct {
	msgID      string
	receivedAt time.Time
}

// Start 装配桥与通道;已配置凭证(AppID/AppSecret)则自动启动 WS gateway;未配置提示 /qq login。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	mode := im.AccessPairing
	baseURL := qqbot.DefaultBaseURL
	tokenURL := qqbot.DefaultTokenURL
	if m != nil && m.Data != nil {
		if v, ok := m.Data["mode"].(string); ok && v != "" {
			mode = im.AccessMode(v)
		}
		if v, ok := m.Data["base_url"].(string); ok && v != "" {
			baseURL = v
		}
		if v, ok := m.Data["token_url"].(string); ok && v != "" {
			tokenURL = v
		}
	}
	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		return nil, err
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		return nil, err
	}
	var turn sdk.TurnControl
	_ = c.Inject("ctx.turnControl", &turn)
	var cmds sdk.CommandRegistry
	_ = c.Inject("ctx.commands", &cmds)

	store := qqbot.NewStore(credsPath())
	creds, err := store.Load()
	if err != nil {
		return nil, err
	}
	tr := &qqTransport{name: channelName, store: store, creds: creds,
		baseURL: baseURL, tokenURL: tokenURL, lastError: "未配置(执行 /qq login)",
		budget:  newActiveQuota(quotaPath()),
		replies: make(map[string]*replyCtx), seq: make(map[string]uint64),
		ledger:   newDeliveryLedger(outboxPath())}
	b := im.New(c, loop, sessions, tr, im.Options{
		Mode:  mode,
		Allow:       creds.Allow,  // 已授权用户持久恢复
		AllowGroups: creds.Groups, // 已授权群持久恢复(群维度授权)
		// P1 会话绑定:chat→宿主会话映射落盘(重启恢复绑定)
		SessionBindPath: sessionBindPath(),
		// P2 §7.5:被动回复窗口 5min —— 回合超时即转后台通知,完成经门控投递
		AsyncAfter: 5 * time.Minute,
	})
	tr.bridge = b
	// 授权变化持久化(/im pair 批准、allow/revoke):写回凭证 store,重启恢复。
	b.Access().SetOnChange(func() {
		allow := b.Access().List()
		groups := b.Access().Groups()
		tr.mu.Lock()
		tr.creds.Allow = allow
		tr.creds.Groups = groups
		err := tr.store.Save(tr.creds)
		tr.mu.Unlock()
		if err != nil {
			tr.setLastError("授权持久化失败: " + err.Error())
		}
	})
	if turn != nil {
		b.SetTurnControl(turn)
	}
	// 已配置凭证 → 自动启动 gateway;未配置提示(QQ 无扫码,需 /qq login 填 AppID/AppSecret)
	if creds.Configured() {
		tr.startGateway()
	}
	// ctx.imChannels:Web/桌面设置面板 IM 通道状态(P3 三端融合;只读展示)
	if err := c.Provide("ctx.imChannels", imChannelStatus{tr: tr, bridge: b}); err != nil {
		return nil, err
	}
	// ctx.confirm = IM 桥(单 profile 自提供;P3 融合:装配 host-confirm-fusion 时
	// 改为注册呈现者,与 web 等渠道并存同卡——不再 Provide 防同名冲突)
	var confirmReg sdk.Disposer = func() {}
	var questionReg sdk.Disposer = func() {}
	var fusion sdk.ConfirmFusion
	if err := c.Inject("ctx.confirmFusion", &fusion); err == nil && fusion != nil {
		confirmReg = fusion.Register("im-qq", b)
		// P3 语义交互:同一桥作为提问呈现者注册(与确认同管道,首答生效)
		var qfusion sdk.QuestionService
		if err := c.Inject("ctx.question", &qfusion); err == nil && qfusion != nil {
			questionReg = qfusion.RegisterQuestioner("im-qq", b)
		}
	} else {
		if err := c.Provide("ctx.confirm", b); err != nil {
			return nil, err
		}
		// 单 profile(无 fusion):桥自身提供结构化提问服务
		if err := c.Provide("ctx.question", im.NewQuestionService(b)); err != nil {
			return nil, err
		}
	}
	// 命令注册:桥自带 /stop /im(pair/status/list)+ 通道命令 /qq login|status
	var ds []sdk.Disposer
	if cmds != nil {
		d, err := b.RegisterCommands(cmds)
		if err != nil {
			return nil, err
		}
		ds = append(ds, d)
		d2, err := cmds.Register(sdk.CommandSpec{
			Name:  "qq",
			Usage: "/qq status|login|env",
			Desc:  "QQ 官方 Bot 通道:配置(AppID/AppSecret)/状态",
			Run:   func(args []string) (string, error) { return tr.qqCmd(context.Background(), args) },
			// 二级选项:status 查看 / login 填凭证(login 再分两级输入 AppID、AppSecret)
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{
						{Value: "status", Desc: "查看配置/网关/已授权"},
						{Value: "login", Desc: "填写 AppID/AppSecret 并启动网关"},
						{Value: "env", Desc: "切换 OpenAPI 环境(未上架机器人联调用 sandbox)"},
					}
				}},
				{FreeArgs: func(picked []string) []string {
					// picked = [命令名, 第一级值, ...](picked[0] 恒为命令名)
					if len(picked) >= 2 && picked[1] == "login" {
						return []string{"AppID", "AppSecret"}
					}
					return nil // status:无参数,直接执行
				}},
			},
		})
		if err != nil {
			return nil, err
		}
		ds = append(ds, d2)
	}
	return func() {
		confirmReg()
		questionReg()
		tr.stopGateway()
		for _, d := range ds {
			d()
		}
	}, nil
}

// credsPath 凭证路径:$GAH_HOME/config/qqbot.yaml(便携纪律;空 GAH_HOME 兜底 TempDir)。
func credsPath() string {
	home := os.Getenv("GAH_HOME")
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, "config", "qqbot.yaml")
}

// quotaPath 主动配额状态路径:$GAH_HOME/config/qqbot-quota.yaml(0600;重启不超发)。
func quotaPath() string {
	home := os.Getenv("GAH_HOME")
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, "config", "qqbot-quota.yaml")
}

// outboxPath 滞留 ledger 落盘路径:$GAH_HOME/config/qqbot-outbox.yaml(0600;重启不丢滞留)。
func outboxPath() string {
	home := os.Getenv("GAH_HOME")
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, "config", "qqbot-outbox.yaml")
}

// sessionBindPath chat↔宿主会话绑定映射路径:$GAH_HOME/config/im-sessions.yaml
// (P1 会话绑定命令面;routeKey 含 \x00 经 JSON 转义安全往返,0600 原子写)。
func sessionBindPath() string {
	home := os.Getenv("GAH_HOME")
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, "config", "im-sessions.yaml")
}

// qqTransport 实现 im.Transport:WS 收事件 + REST 被动回复。
type qqTransport struct {
	name     string
	store    *qqbot.Store
	creds    *qqbot.Credentials
	baseURL  string
	tokenURL string
	bridge   *im.Bridge

	mu        sync.Mutex
	gateway   *qqbot.Gateway
	client    *qqbot.Client
	stop      context.CancelFunc // gateway Run 取消
	running   bool
	lastError string
	botOpenID string // READY d.user.id(群 @ 过滤:mentions 需含机器人)
	replies   map[string]*replyCtx
	seq       map[string]uint64  // ChatID → msg_seq(与 msg_id 联合幂等,自增)
	typingCtl context.CancelFunc // 回合中 input_notify 周期刷新控制器(回合结束取消)
	budget    *activeQuota       // 主动消息配额记账(私信主动 2 条/天/用户;落盘重启不超发)
	ledger    *deliveryLedger // 滞留 ledger(落盘重启不丢;被动失效/频控/配额耗尽时暂存,下次入站补发)
}

func (t *qqTransport) Name() string { return t.name }

// startGateway 启动 WS gateway(幂等;基于当前 creds 重建 token 源/客户端)。
func (t *qqTransport) startGateway() {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return
	}
	creds := *t.creds
	baseURL, tokenURL := t.baseURL, t.tokenURL
	if creds.BaseURL != "" { // 凭证显式设置(如 /qq env sandbox)优先
		baseURL = creds.BaseURL
	}
	t.mu.Unlock()
	ts := qqbot.NewTokenSource(creds.AppID, creds.AppSecret)
	ts.URL = tokenURL
	cli := qqbot.NewClient(ts)
	if baseURL != qqbot.DefaultBaseURL {
		cli.WithBaseURL(baseURL)
	}
	gw := qqbot.NewGateway(func(ctx context.Context) (string, error) {
		tk, err := ts.Token(ctx)
		if err != nil {
			return "", err
		}
		return qqbot.IdentifyToken(tk), nil
	}, func(e qqbot.Event) { t.onEvent(e) })
	if baseURL != qqbot.DefaultBaseURL {
		gw.BaseURL = baseURL
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.gateway, t.client, t.stop = gw, cli, cancel
	t.running = true
	t.lastError = ""
	t.mu.Unlock()
	go func() {
		if err := gw.Run(ctx); err != nil {
			if err != context.Canceled {
				t.setLastError("gateway 停止: " + err.Error())
			}
		}
		t.mu.Lock()
		t.running = false
		t.mu.Unlock()
	}()
}

// stopGateway 停止 gateway(幂等)。
func (t *qqTransport) stopGateway() {
	t.mu.Lock()
	c := t.stop
	t.gateway, t.client, t.stop = nil, nil, nil
	t.running = false
	t.mu.Unlock()
	if c != nil {
		c()
	}
}

func (t *qqTransport) setLastError(s string) {
	t.mu.Lock()
	t.lastError = s
	t.mu.Unlock()
}

// onEvent gateway 事件回调(reader goroutine 内同步调用,必须快:仅做解析与转派 goroutine)。
func (t *qqTransport) onEvent(e qqbot.Event) {
	switch e.Type {
	case qqbot.EventReady: // 记录机器人自身 openid(群 @ 过滤);状态在线
		var rd qqbot.Ready
		if err := json.Unmarshal(e.Data, &rd); err == nil {
			t.mu.Lock()
			t.botOpenID = rd.User.ID
			t.lastError = ""
			t.mu.Unlock()
		}
	case qqbot.EventC2CMessage:
		var m qqbot.C2CMessage
		if err := json.Unmarshal(e.Data, &m); err == nil {
			go t.handleC2C(&m) // 独立 goroutine(回合/Confirm 等待不阻塞网关读循环)
		}
	case qqbot.EventGroupAtMsg:
		var m qqbot.GroupAtMessage
		if err := json.Unmarshal(e.Data, &m); err == nil {
			go t.handleGroup(&m)
		}
	}
}

// handleC2C 单聊消息:缓存被动回复 msg_id → 桥处理。panic 隔离。
func (t *qqTransport) handleC2C(m *qqbot.C2CMessage) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[qq] 消息处理 panic(已隔离): %v\n", r)
		}
	}()
	if m.Author.UserOpenID == "" || m.Content == "" {
		return
	}
	route := im.Route{Channel: t.name, UserID: m.Author.UserOpenID, ChatID: m.Author.UserOpenID}
	t.cacheReply(route.ChatID, m.ID)
	t.flushOutbox(context.Background(), route) // 滞留内容先被动补发(新 msg_id 窗口)
	_ = t.bridge.HandleInbound(context.Background(), im.Inbound{Route: route, MsgID: "c2c:" + m.ID, Text: m.Content})
}

// handleGroup 群 @ 消息:仅 @ 机器人(mentions 含机器人)才处理(官方已按事件类型过滤,防御加强)。
func (t *qqTransport) handleGroup(m *qqbot.GroupAtMessage) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[qq] 群消息处理 panic(已隔离): %v\n", r)
		}
	}()
	if m.GroupOpenID == "" || m.Author.MemberOpenID == "" || m.Content == "" {
		return
	}
	if !t.mentionsBot(m) {
		return // 非 @ 机器人(模拟器/异常推送):丢弃
	}
	route := im.Route{Channel: t.name, UserID: m.Author.MemberOpenID, ChatID: m.GroupOpenID}
	t.cacheReply(route.ChatID, m.ID)
	t.flushOutbox(context.Background(), route) // 滞留内容先被动补发(新 msg_id 窗口)
	_ = t.bridge.HandleInbound(context.Background(), im.Inbound{Route: route, MsgID: "grp:" + m.ID, Text: m.Content})
}

// mentionsBot 群消息是否 @ 了机器人:botOpenID 已知时严格校验;未知(未 Ready)放行。
func (t *qqTransport) mentionsBot(m *qqbot.GroupAtMessage) bool {
	t.mu.Lock()
	bot := t.botOpenID
	t.mu.Unlock()
	if bot == "" {
		return true
	}
	for _, mt := range m.Mentions {
		if mt.ID == bot || mt.MemberOpenID == bot || mt.UserOpenID == bot {
			return true
		}
	}
	return false
}

// cacheReply 缓存 ChatID → 最近被动消息(msg_id 5min 窗口);群/单聊共用。
func (t *qqTransport) cacheReply(chatID, msgID string) {
	if chatID == "" || msgID == "" {
		return
	}
	t.mu.Lock()
	t.replies[chatID] = &replyCtx{msgID: msgID, receivedAt: time.Now()}
	t.mu.Unlock()
}

// SendText 出站回推(§7.5 时效/配额 + §7.6 呈现)。投递预算:
//   - 被动窗口内(缓存 msg_id,5min):完整呈现(富文本单条 md/纯文本/超长分块),带 msg_id+msg_seq,
//     不耗主动配额;多块仅被动窗口内允许(窗口靠 msg_id 逐个回复,官方 5min)。
//   - 无被动上下文 / 被动过期(40034128/40034005)→ 主动单条补一次:走配额记账
//     (私信主动 2 条/天/用户;合并 ≤4000 单条),配额耗尽 → 静默滞留 outbox,
//     下次该会话用户消息被动窗口内补发(结果入会话,下次回送)。
//   - 频控(40034100/http 429)→ 立即停发不轰炸(不做“发到成功为止”),整段滞留 + lastError。
//
// 滞留不落盘(重启丢失;会话日志仍完整,语义 = 下次被动回送)。
func (t *qqTransport) SendText(ctx context.Context, to im.Route, text string) error {
	t.mu.Lock()
	cli := t.client
	if cli == nil {
		t.mu.Unlock()
		return fmt.Errorf("qq: 网关未运行(未配置 /qq login?)")
	}
	msgID := ""
	if rc, ok := t.replies[to.ChatID]; ok && time.Since(rc.receivedAt) <= replyWindow {
		msgID = rc.msgID
	}
	baseSeq := t.seq[to.ChatID]
	baseMsgID := msgID
	t.mu.Unlock()
	isGroup := to.ChatID != to.UserID

	// 组装呈现清单(超限纯文本分块 / 富文本 markdown 单条 / 纯文本单条)
	msgs := buildTextMessages(text)
	// 审批确认文本 → 附加键盘(批准/拒绝按钮;action.type=2 回复消息,点击以按钮文本
	// 回消息 → 落入现有文字 y/n 管线,零新增回调协议)。键盘消息紧随说明文本。
	if strings.HasPrefix(text, confirmPrefix) {
		msgs = append(msgs, qqbot.SendMessage{MsgType: qqbot.MsgTypeKeyboard, Keyboard: confirmKeyboard()})
	}
	trunc := len(msgs) > qqMaxChunks
	if trunc {
		msgs = msgs[:qqMaxChunks]
	}
	sendOne := func(msg qqbot.SendMessage, seq uint64, passive bool) error {
		if passive {
			msg.MsgID = baseMsgID
		}
		msg.MsgSeq = seq
		if isGroup {
			return cli.SendGroupMessage(ctx, to.ChatID, msg)
		}
		return cli.SendC2CMessage(ctx, to.ChatID, msg)
	}
	deliverActive := func() error {
		if !t.budget.Allow(to.SenderKey()) {
			t.stash(to.ChatID, text) // 配额耗尽:静默滞留,下次被动补发
			return nil
		}
		if err := sendOne(activeOneMessage(text), 0, false); err != nil {
			if qqbot.IsRateLimited(err) {
				t.setLastError("主动发送频控: " + err.Error())
				t.stash(to.ChatID, text)
				return nil
			}
			return err
		}
		_ = t.budget.Consume(to.SenderKey())
		return nil
	}

	if baseMsgID == "" {
		return deliverActive() // 无被动上下文(回合 >5min 等):主动配额补一次
	}
	for i, msg := range msgs {
		seq := baseSeq + uint64(i+1)
		if err := sendOne(msg, seq, true); err != nil {
			switch {
			case qqbot.IsRateLimited(err): // 频控:立即停发,整段滞留(不做“发到成功为止”)
				t.setLastError("发送频控: " + err.Error())
				t.stash(to.ChatID, text)
				return nil
			case qqbot.IsPassiveExpired(err): // 被动窗口错过 → 主动配额补一次
				return deliverActive()
			default:
				if trunc {
					_ = sendOne(qqbot.SendMessage{MsgType: qqbot.MsgTypeText, Content: "⚠️ 回复过长已截断;请回复 continue 获取剩余内容"}, 0, true)
				}
				return err
			}
		}
		if i < len(msgs)-1 {
			time.Sleep(qqChunkGap)
		}
	}
	t.mu.Lock()
	t.seq[to.ChatID] = baseSeq + uint64(len(msgs))
	t.mu.Unlock()
	if trunc {
		err := sendOne(qqbot.SendMessage{MsgType: qqbot.MsgTypeText, Content: "⚠️ 回复过长已截断;请回复 continue 获取剩余内容"}, 0, true)
		return err
	}
	return nil
}

// buildTextMessages 按 §7.6 呈现组装发送清单(富文本单条 md / 纯文本 / 超长纯文本分块)。
// 分块走出站预算层 im.SplitText(rune 安全;与微信线共用同一实现)。
func buildTextMessages(text string) []qqbot.SendMessage {
	if len([]rune(text)) > qqChunkLimit {
		var out []qqbot.SendMessage
		for _, ch := range im.SplitText(text, qqChunkLimit) {
			out = append(out, qqbot.SendMessage{MsgType: qqbot.MsgTypeText, Content: ch})
		}
		return out
	}
	if isRichText(text) {
		return []qqbot.SendMessage{{MsgType: qqbot.MsgTypeMarkdown, Markdown: &qqbot.Markdown{Content: text}}}
	}
	return []qqbot.SendMessage{{MsgType: qqbot.MsgTypeText, Content: text}}
}

// confirmPrefix 审批确认文本前缀(桥 Present;命中则附加键盘)。
const confirmPrefix = "🔐 需要确认: "

// confirmKeyboard 审批键盘:批准/拒绝 reply 按钮(点击以按钮文本回复 → y/n 词表匹配)。
func confirmKeyboard() *qqbot.Keyboard {
	return &qqbot.Keyboard{Content: &qqbot.KeyboardContent{Rows: []qqbot.KeyboardRow{{Buttons: []qqbot.KeyboardButton{
		{ID: "approve", RenderData: qqbot.ButtonRender{Label: "批准", Style: 3}, Action: qqbot.ButtonAction{Type: 2, Permission: qqbot.ButtonPermission{Type: 1}}},
		{ID: "deny", RenderData: qqbot.ButtonRender{Label: "拒绝", Style: 4}, Action: qqbot.ButtonAction{Type: 2, Permission: qqbot.ButtonPermission{Type: 1}}},
	}}}}}
}

// truncTail 主动截断尾部提示(预留空间保证整体 ≤4000)。
const truncTail = "…(已截断,请发消息继续)"

// activeOneMessage 主动投递合并单条(主动配额 2 条/天稀缺:多块会耗尽预算,超长截断 + 提示)。
func activeOneMessage(text string) qqbot.SendMessage {
	rs := []rune(text)
	if len(rs) <= qqChunkLimit {
		return qqbot.SendMessage{MsgType: qqbot.MsgTypeText, Content: text}
	}
	keep := qqChunkLimit - len([]rune(truncTail))
	if keep < 0 {
		keep = 0
	}
	return qqbot.SendMessage{MsgType: qqbot.MsgTypeText, Content: string(rs[:keep]) + truncTail}
}

// dupPrefix 重投提示(P2 二期):此前投递结果未确认,至少一次语义下可能已送达。
const dupPrefix = "♻️ 可能重复(此前投递未确认):\n"

// stash 滞留整段文本(P2 delivery ledger:落盘重启不丢;下次该会话入站 flush 补发)。
func (t *qqTransport) stash(chatID, text string) {
	if chatID == "" || text == "" {
		return
	}
	t.ledger.Stash(chatID, text)
}

// flushOutbox 入站后先补发滞留内容(此时刚缓存新 msg_id,被动窗口内);失败放回下次。
// 此前失败过的内容补发时加 dupPrefix(表示可能已送达,防用户误判重复消息)。
func (t *qqTransport) flushOutbox(ctx context.Context, route im.Route) {
	pend, attempts := t.ledger.Take(route.ChatID)
	if pend == "" {
		return
	}
	send := pend
	if attempts > 0 {
		send = dupPrefix + send
	}
	if err := t.SendText(ctx, route, send); err != nil {
		t.ledger.Stash(route.ChatID, pend) // 放回原文(前缀发送时拼,不写回,防累积)
	}
}

// isRichText 呈现决策:含 markdown 结构(代码围栏/行首列表)的正文走 markdown 渲染(msg_type=2);
// 纯短句/普通段落用纯文本(§7.6:避免每段都触发富文本气泡)。克制匹配,不引入全局规则误伤。
func isRichText(s string) bool {
	if strings.Contains(s, "```") {
		return true
	}
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(t, "-") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "+"):
			// 无序列表需后随空白或符号才算(避免把 -x 当列表)
			if len(t) == 1 || t[1] == ' ' {
				return true
			}
		case len(t) >= 2 && t[0] >= '0' && t[0] <= '9' && (t[1] == '.' || t[1] == ')'):
			return true // 有序列表 1. / 1)
		}
	}
	return false
}

// ShowTyping im.TypingAware:回合开始同步首发 input_notify(1,正在输入)+ ~30s 周期刷新保持
// (QQ input_notify 状态非持续);回合结束 StopTyping 取消并发 input_type=0。best-effort(失败静默)。
func (t *qqTransport) ShowTyping(_ context.Context, r im.Route) error {
	t.mu.Lock()
	if t.typingCtl != nil {
		t.typingCtl() // 上个回合残留刷新先停
	}
	c2, cancel := context.WithCancel(context.Background())
	t.typingCtl = cancel
	t.mu.Unlock()
	t.sendInputState(r, 1) // 同步首发:确保回合内 mock/e2e 必见 show
	go func() {
		tk := time.NewTicker(typingInterval)
		defer tk.Stop()
		for {
			select {
			case <-c2.Done():
				return
			case <-tk.C:
				t.sendInputState(r, 1)
			}
		}
	}()
	return nil
}

// StopTyping im.TypingAware:回合结束取消刷新并发 input_type=0(取消输入状态)。
func (t *qqTransport) StopTyping(_ context.Context, r im.Route) error {
	t.mu.Lock()
	c := t.typingCtl
	t.typingCtl = nil
	t.mu.Unlock()
	if c != nil {
		c()
	}
	t.sendInputState(r, 0)
	return nil
}

// sendInputState 发一次 input_notify(单聊/群按 route 分派;带缓存 msg_id 被动;失败静默)。
func (t *qqTransport) sendInputState(r im.Route, inputType int) {
	t.mu.Lock()
	cli := t.client
	if cli == nil {
		t.mu.Unlock()
		return
	}
	msgID := ""
	if rc, ok := t.replies[r.ChatID]; ok && time.Since(rc.receivedAt) <= replyWindow {
		msgID = rc.msgID
	}
	t.mu.Unlock()
	notify := &qqbot.InputNotify{InputType: inputType, InputSecond: 60}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if r.ChatID != r.UserID { // 群
		_ = cli.SendGroupMessage(ctx, r.ChatID, qqbot.SendMessage{MsgType: qqbot.MsgTypeInput, InputNotify: notify, MsgID: msgID})
		return
	}
	_ = cli.SendInputState(ctx, r.UserID, inputType, 60, msgID, 0)
}



// qqCmd /qq 命令:login/status。
func (t *qqTransport) qqCmd(_ context.Context, args []string) (string, error) {
	if len(args) == 0 || args[0] == "status" {
		return t.statusText(), nil
	}
	if args[0] == "env" {
		return t.envCmd(args)
	}
	if args[0] != "login" {
		return "", fmt.Errorf("用法: /qq status|login|env")
	}
	if len(args) >= 3 && args[1] != "" && args[2] != "" {
		if err := t.login(args[1], args[2]); err != nil {
			return "", err
		}
		return "已保存 QQ 凭证并启动网关(接入官方 api-v2)。状态查询 /qq status。", nil
	}
	// 无参 = 输出接入指引(AppID/AppSecret 从开放平台获取;对齐 /wechat login 的"输出指引"职责)
	return "QQ 机器人凭证(AppID/AppSecret)获取:开放平台 q.qq.com → 开发 → 开发设置。\n" +
		"填入方式(二选一):\n" +
		"  1) /qq login <AppID> <AppSecret>\n" +
		"  2) 编辑配置文件: " + t.store.Path + "\n" +
		"(凭证 0600 落盘随 $GAH_HOME 迁移;access_token 运行时换取不落盘)", nil
}

// envCmd /qq env:查看/切换 OpenAPI 根(未上架机器人需沙箱环境联调)。
func (t *qqTransport) envCmd(args []string) (string, error) {
	t.mu.Lock()
	cur := t.baseURL
	if t.creds != nil && t.creds.BaseURL != "" {
		cur = t.creds.BaseURL
	}
	t.mu.Unlock()
	if len(args) < 2 {
		return fmt.Sprintf("当前 OpenAPI 根: %s\n切换: /qq env official|sandbox(或直接给 URL)", cur), nil
	}
	var u string
	switch args[1] {
	case "official":
		u = qqbot.DefaultBaseURL
	case "sandbox":
		u = "https://sandbox.api.sgroup.qq.com"
	default:
		u = args[1]
	}
	t.mu.Lock()
	if t.creds == nil {
		t.creds = &qqbot.Credentials{}
	}
	t.creds.BaseURL = u
	creds := *t.creds
	store := t.store
	t.mu.Unlock()
	if err := store.Save(&creds); err != nil {
		return "", fmt.Errorf("qq: 保存环境失败: %w", err)
	}
	t.stopGateway()
	if creds.Configured() {
		t.startGateway()
	}
	return "已切换 OpenAPI 根: " + u + "(网关已重启;状态查询 /qq status)", nil
}

// login 保存凭证并启动网关(AppSecret 属密钥,落盘收紧 0600)。
func (t *qqTransport) login(appID, secret string) error {
	if appID == "" || secret == "" {
		return fmt.Errorf("qq: AppID/AppSecret 不能为空")
	}
	if t.creds == nil {
		t.creds = &qqbot.Credentials{}
	}
	t.mu.Lock()
	t.creds.AppID = appID
	t.creds.AppSecret = secret
	if t.creds.BaseURL == "" {
		t.creds.BaseURL = t.baseURL
	}
	store := t.store
	t.mu.Unlock()
	if err := store.Save(t.creds); err != nil {
		return fmt.Errorf("qq: 保存凭证失败: %w", err)
	}
	t.startGateway()
	// 即时校验:换取一次 access_token,把失败原因直接返回(不再只落 lastError 静默)。
	// tokenURL 为空(嵌入/单测未配置)则跳过校验——生产由插件壳恒设默认端点。
	if t.tokenURL == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ts := qqbot.NewTokenSource(appID, secret)
	ts.URL = t.tokenURL
	if _, err := ts.Token(ctx); err != nil {
		return fmt.Errorf("凭证已保存,但校验 access_token 失败: %w\n"+
			"排查:① AppID/AppSecret 抄错/被重置(开放平台 开发设置);"+
			"② 机器人未上架时需沙箱环境 → /qq env sandbox;"+
			"③ 网络可达 bots.qq.com", err)
	}
	return nil
}

// imChannelStatus sdk.IMChannelService 适配(web 面板 IM 通道状态)。
type imChannelStatus struct {
	tr     *qqTransport
	bridge *im.Bridge
}

// Status 聚合渠道状态(实时读取;state:online>running>configuring>offline)。
func (a imChannelStatus) Status() []sdk.IMChannelStatus {
	tr := a.tr
	tr.mu.Lock() // statusText 内部自持锁,勿锁内调用——字段锁外组装
	state := "offline"
	if tr.running {
		state = "running"
		if tr.gateway != nil && tr.gateway.Online() {
			state = "online"
		}
	}
	if tr.creds == nil || !tr.creds.Configured() {
		state = "configuring"
	}
	lastErr := tr.lastError
	tr.mu.Unlock()
	detail := tr.statusText() // 锁外取全文
	auth := 0
	if a.bridge != nil {
		auth = len(a.bridge.Access().List())
	}
	return []sdk.IMChannelStatus{{
		Channel: tr.name, State: state, Detail: detail, Error: lastErr, Authorized: auth,
	}}
}

// statusText 状态文本。
func (t *qqTransport) statusText() string {
	t.mu.Lock()
	gwPtr := t.gateway
	cfg := "未配置"
	appID := ""
	if t.creds != nil && t.creds.Configured() {
		cfg = "已配置"
		appID = mask(t.creds.AppID)
	}
	gw := "停"
	if t.running {
		gw = "运行"
		if t.gateway != nil && t.gateway.Online() {
			gw = "在线"
		}
	}
	allowed := 0
	if t.creds != nil {
		allowed = len(t.creds.Allow)
	}
	if t.bridge != nil {
		allowed = len(t.bridge.Access().List())
	}
	if t.creds != nil && t.creds.BaseURL != "" {
		cfg += "@" + t.creds.BaseURL
	}
	lastErr := t.lastError
	t.mu.Unlock()
	// gateway 内部诊断(断线/鉴权失败等)合并展示(锁外调用防锁序问题)
	if gwPtr != nil {
		if ge := gwPtr.LastError(); ge != "" {
			if lastErr != "" {
				lastErr = ge + " | " + lastErr
			} else {
				lastErr = ge
			}
		}
	}
	if lastErr == "" {
		lastErr = "无"
	}
	return fmt.Sprintf("qq: %s(%s) 网关=%s 已授权=%d\n最近: %s", cfg, appID, gw, allowed, lastErr)
}

// mask 凭证掩码(前 4 位 + 长度,防全量泄露)。
func mask(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:4] + "…" + fmt.Sprintf("(%d)", len(s))
}
