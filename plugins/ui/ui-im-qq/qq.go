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
	mediaMaxMB := 0 // MED-1:出站媒体大小上限(MB;0 = 默认 20)
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
		switch v := m.Data["media_max_mb"].(type) {
		case int:
			mediaMaxMB = v
		case float64:
			mediaMaxMB = int(v)
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
		ledger:     newDeliveryLedger(outboxPath()),
		remainder:  make(map[string]string),
		mediaInfos: map[string]mediaInfoEntry{}}
	b := im.New(c, loop, sessions, tr, im.Options{
		Mode:        mode,
		Allow:       creds.Allow,  // 已授权用户持久恢复
		AllowGroups: creds.Groups, // 已授权群持久恢复(群维度授权)
		// P1 会话绑定:chat→宿主会话映射落盘(重启恢复绑定)
		SessionBindPath: sessionBindPath(),
		// MED-1:出站媒体大小上限(data.media_max_mb,默认 20)
		MediaMaxMB: mediaMaxMB,
		// P2 §7.5:被动回复窗口 5min —— 回合超时即转后台通知,完成经门控投递
		AsyncAfter: 5 * time.Minute,
		// 入站诊断(未授权丢弃/重复丢弃/回合启动)→ 环形缓冲,/qq status 可见
		Diag: func(s string) { tr.diagf("%s", s) },
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
	// G-E5-3:受控出站面(im_send/im_status 经此投递;工具默认不注册,见 plugins/tool/tool-im)
	if err := c.Provide("ctx.imControl", b); err != nil {
		return nil, err
	}
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
		// G-E5-4:包装 ObservableQuestion → 单 profile 也广播 question/requested ↔ resolved
		if err := c.Provide("ctx.question", sdk.ObservedQuestion(c, "im-qq", im.NewQuestionService(b))); err != nil {
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
			Name: "qq",
			// 三级逐级确认:L1 子命令枚举 → L2(env:环境枚举 / login:AppID、AppSecret 逐步输入自由序列);
			// status 无二级参数(选完即执行)。
			Usage: "/qq status|login|env [official|sandbox]",
			Desc:  "QQ 官方 Bot 通道:配置(AppID/AppSecret)/状态/环境",
			Run:   func(args []string) (string, error) { return tr.qqCmd(context.Background(), args) },
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{
						{Value: "status", Desc: "查看配置/环境/网关/诊断"},
						{Value: "login", Desc: "填写 AppID/AppSecret 并启动网关"},
						{Value: "env", Desc: "切换 OpenAPI 环境(群聊测试需 sandbox)"},
					}
				}},
				{
					// 环境枚举(env 路径);login 路径 Options 空 → 回退到自由参数序列
					Options: func(picked []string) []sdk.Option {
						if len(picked) >= 2 && picked[1] == "env" {
							return []sdk.Option{
								{Value: "official", Desc: "正式环境(api.bot.qq.com;已上架机器人;群聊需企业公开服务)"},
								{Value: "sandbox", Desc: "沙箱环境(sandbox.api.sgroup.qq.com;个人开发者群聊唯一路径)"},
							}
						}
						return nil
					},
					FreeArgs: func(picked []string) []string {
						// picked = [命令名, 第一级值, ...](picked[0] 恒为命令名)
						if len(picked) >= 2 && picked[1] == "login" {
							return []string{"AppID", "AppSecret"}
						}
						return nil // status/env 无自由参数(env 走上级枚举)
					},
				},
			},
		})
		if err != nil {
			return nil, err
		}
		ds = append(ds, d2)
	}
	// D5:文档预览意图(doc/open)→ 通道文本降级回推(前 N 行 + 页事实;无活跃会话静默跳过)
	ds = append(ds, c.Subscribe(sdk.EventDocOpen, func(_ context.Context, ev *sdk.Event) error {
		switch p := ev.Payload.(type) {
		case sdk.DocOpenEvent:
			b.HandleDocOpen(context.Background(), p)
		case *sdk.DocOpenEvent:
			b.HandleDocOpen(context.Background(), *p)
		}
		return nil
	}))
	// G-E5-4:交互事件观察面(其它渠道已处理的提问/审批 → 回推提示;本渠道结论跳过)
	ds = append(ds, b.WatchInteraction(c, "im-qq"))
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

	mu         sync.Mutex
	gateway    *qqbot.Gateway
	client     *qqbot.Client
	stop       context.CancelFunc // gateway Run 取消
	running    bool
	lastError  string
	botOpenID  string // READY d.user.id(群 @ 过滤:mentions 需含机器人)
	replies    map[string]*replyCtx
	seq        map[string]uint64         // ChatID → msg_seq(与 msg_id 联合幂等,自增)
	typingCtl  context.CancelFunc        // 回合中 input_notify 周期刷新控制器(回合结束取消)
	budget     *activeQuota              // 主动消息配额记账(私信主动 2 条/天/用户;落盘重启不超发)
	ledger     *deliveryLedger           // 滞留 ledger(落盘重启不丢;被动失效/频控/配额耗尽时暂存,下次入站补发)
	remainder  map[string]string         // chatID → 被截断的剩余文本(用户回 continue 时被动续发)
	diagRing   []string                  // 最近入站诊断(环形;/qq status 展示 + GAH_QQ_DEBUG=1 打 stderr)
	gen        int64                     // 网关世代号:start/stop 递增;旧 goroutine 收尾仅当同世代才改状态(防覆盖)
	evCounts   map[string]int            // 事件类型计数(诊断:平台是否推事件——群@=0 即平台侧未推)
	conn       sdk.IMConnectStatus       // E0/E2:连接卡相位(表单校验中/失败原因)
	mediaInfos map[string]mediaInfoEntry // MED-2:file_info 缓存(TTL 内复用,避免重复上传)
	// mediaAPIOverride 仅测试注入(生产 nil → 用 client)。
	mediaAPIOverride qqMediaAPI
}

// maskedSentinel 前端提交"未改动"密钥时的哨兵值(不回显明文,沿用已配置值)。
const maskedSentinel = "__keep__"

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
	t.running = true
	t.gen++ // 本世代:旧 goroutine 的收尾不得再改动状态(见 gatewayStopped)
	gen := t.gen
	t.lastError = ""
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
	t.mu.Unlock()
	t.diagf("网关启动 环境=%s base=%s", envName(baseURL), baseURL)
	go func() { t.gatewayStopped(gen, gw.Run(ctx)) }()
}

// gatewayStopped 网关 goroutine 收尾:仅当仍是**本世代**(未被 stopGateway/新 startGateway 取代)
// 才更新 running/lastError。否则旧 goroutine 会覆盖新网关状态——实测现象:切环境后事件仍在到达
// (诊断有 READY/入站)但 `/qq status` 误报"网关=停"。
func (t *qqTransport) gatewayStopped(gen int64, err error) {
	t.mu.Lock()
	if t.gen != gen {
		t.mu.Unlock()
		return
	}
	t.running = false
	if err != nil && err != context.Canceled {
		t.lastError = "gateway 停止: " + err.Error()
	}
	t.mu.Unlock()
	if err != nil && err != context.Canceled {
		t.diagf("网关停止: %v", err)
	}
}

// stopGateway 停止 gateway(幂等)。
func (t *qqTransport) stopGateway() {
	t.mu.Lock()
	c := t.stop
	t.gateway, t.client, t.stop = nil, nil, nil
	t.running = false
	t.gen++ // 使在途 goroutine 的收尾失效(不覆盖后续新网关状态)
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

// diagf 记录一条入站/出站诊断:始终入环形缓冲(/qq status 可见);设 GAH_QQ_DEBUG=1
// 时同时打 stderr。真机排障核心:群 @ 无反应时用它区分
// "事件未到达 ← 平台侧" / "被判定丢弃 ← 代码" / "已交桥但未授权"。
func (t *qqTransport) diagf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	t.mu.Lock()
	t.diagRing = append(t.diagRing, time.Now().Format("15:04:05")+" "+msg)
	if len(t.diagRing) > 40 {
		t.diagRing = t.diagRing[len(t.diagRing)-40:]
	}
	t.mu.Unlock()
	if os.Getenv("GAH_QQ_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[qq] %s\n", msg)
	}
}

// countEvent 事件类型计数(诊断用;不受诊断环形滚动影响,可长期印证平台是否推过某类事件)。
func (t *qqTransport) countEvent(kind string) {
	t.mu.Lock()
	if t.evCounts == nil {
		t.evCounts = map[string]int{}
	}
	t.evCounts[kind]++
	t.mu.Unlock()
}

// evCount 读取单个计数。
func (t *qqTransport) evCount(kind string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.evCounts[kind]
}

// diagLines 最近 n 条诊断(不足则全部;无诊断返回空串)。
func (t *qqTransport) diagLines(n int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.diagRing) == 0 {
		return ""
	}
	start := len(t.diagRing) - n
	if start < 0 {
		start = 0
	}
	return strings.Join(t.diagRing[start:], "\n")
}

// onEvent gateway 事件回调(reader goroutine 内同步调用,必须快:仅做解析与转派 goroutine)。
func (t *qqTransport) onEvent(e qqbot.Event) {
	switch e.Type {
	case qqbot.EventReady:
		t.countEvent("ready") // 记录机器人自身 openid(群 @ 过滤);状态在线
		var rd qqbot.Ready
		if err := json.Unmarshal(e.Data, &rd); err == nil {
			t.mu.Lock()
			t.botOpenID = rd.User.ID
			t.lastError = ""
			t.mu.Unlock()
			t.diagf("READY 机器人上线 openid=%s ws=%s", rd.User.ID, t.connectedWS())
		}
	case qqbot.EventC2CMessage:
		t.countEvent("c2c")
		var m qqbot.C2CMessage
		if err := json.Unmarshal(e.Data, &m); err == nil {
			t.diagf("事件 C2C_MESSAGE_CREATE msg=%s user=%s len=%d att=%d", m.ID, m.Author.UserOpenID, len([]rune(m.Content)), len(m.Attachments))
			go t.handleC2C(&m) // 独立 goroutine(回合/Confirm 等待不阻塞网关读循环)
		} else {
			t.diagf("事件解析失败 C2C_MESSAGE_CREATE: %v", err)
		}
	case qqbot.EventGroupAtMsg:
		t.countEvent("group")
		var m qqbot.GroupAtMessage
		if err := json.Unmarshal(e.Data, &m); err == nil {
			t.diagf("事件 GROUP_AT_MESSAGE_CREATE msg=%s group=%s member=%s authorid=%s mentions=%d len=%d",
				m.ID, m.GroupOpenID, m.Author.MemberOpenID, m.Author.ID, len(m.Mentions), len([]rune(m.Content)))
			go t.handleGroup(&m)
		} else {
			t.diagf("事件解析失败 GROUP_AT_MESSAGE_CREATE: %v", err)
		}

	default:
		t.countEvent("other")
		// 其余 Dispatch 一律留痕:平台侧权限/入群/审核相关事件(GROUP_ADD_ROBOT/
		// GROUP_DEL_ROBOT/GROUP_MSG_REJECT/FRIEND_ADD/INTERACTION 等)在此可见,
		// 是"群事件完全不来"与"来了但类型不同"的关键区分点。
		t.diagf("事件(未处理) %s size=%d", e.Type, len(e.Data))
	}
}

// handleC2C 单聊消息:缓存被动回复 msg_id → 桥处理。panic 隔离。
func (t *qqTransport) handleC2C(m *qqbot.C2CMessage) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[qq] 消息处理 panic(已隔离): %v\n", r)
		}
	}()
	sender := m.Author.UserOpenID
	if sender == "" {
		sender = m.Author.ID // author 字段差异兜底(官方单聊给 user_openid,防御用 id)
	}
	if sender == "" {
		t.diagf("C2C 丢弃:author 标识缺失(msg=%s)", m.ID)
		return
	}
	if m.Content == "" && len(m.Attachments) == 0 && len(m.MsgElements) == 0 {
		t.diagf("C2C 丢弃:无文本/附件/消息元素(msg=%s user=%s)", m.ID, sender)
		return
	}
	route := im.Route{Channel: t.name, UserID: sender, ChatID: sender}
	t.cacheReply(route.ChatID, m.ID)
	if t.flushRemainder(route, m.Content) {
		return // continue 续取:不走回合
	}
	t.flushOutbox(context.Background(), route) // 滞留内容先被动补发(新 msg_id 窗口)
	atts, note := t.mediaExtract(m.Attachments, sender)
	text := m.Content
	if strings.TrimSpace(text) == "" {
		text = qqbot.ElementsText(m.MsgElements) // 引用/聊天记录类消息(Content 为空)
	}
	if note != "" {
		if text != "" {
			text += "\n" + note
		} else {
			text = note
		}
	}
	if text == "" && len(atts) == 0 {
		return
	}
	_ = t.bridge.HandleInbound(context.Background(), im.Inbound{Route: route, MsgID: "c2c:" + m.ID, Text: text, Attachments: atts})
}

// handleGroup 群 @ 消息:官方 GROUP_AT_MESSAGE_CREATE 仅在用户 @ 机器人时推送,
// 故不做"mentions 是否含机器人"前置否决(见 mentionsBot 注释)。panic 隔离。
func (t *qqTransport) handleGroup(m *qqbot.GroupAtMessage) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[qq] 群消息处理 panic(已隔离): %v\n", r)
		}
	}()
	sender := m.Author.MemberOpenID
	if sender == "" {
		sender = m.Author.ID // 字段差异兼容(官方群事件 member_openid 有值,防御兜底)
	}
	if m.GroupOpenID == "" || sender == "" {
		t.diagf("群丢弃:标识缺失(group=%q member=%q authorid=%q msg=%s)", m.GroupOpenID, m.Author.MemberOpenID, m.Author.ID, m.ID)
		return
	}
	if m.Content == "" && len(m.Attachments) == 0 && len(m.MsgElements) == 0 {
		t.diagf("群丢弃:无文本/附件/消息元素(group=%s member=%s msg=%s)", m.GroupOpenID, sender, m.ID)
		return
	}
	if t.isSelfMessage(m) {
		t.diagf("群丢弃:机器人自身消息(防回环,group=%s member=%s)", m.GroupOpenID, sender)
		return // 自身回环(群内机器人自己发的消息):丢弃防自问自答
	}
	if !t.mentionsBot(m) {
		t.diagf("群丢弃:判定为 @ 其它机器人(mentions=%d group=%s member=%s)", len(m.Mentions), m.GroupOpenID, sender)
		return // 明确判定为 @ 其它机器人(非本机器人):丢弃
	}
	text := m.Content
	// 引用/聊天记录类消息(message_type 103/102)正文可能为空,内容在 msg_elements 内。
	if strings.TrimSpace(text) == "" {
		text = qqbot.ElementsText(m.MsgElements)
	}
	if text == "" && len(m.Attachments) == 0 {
		t.diagf("群丢弃:正文与附件均为空(group=%s member=%s msg=%s)", m.GroupOpenID, sender, m.ID)
		return
	}
	route := im.Route{Channel: t.name, UserID: sender, ChatID: m.GroupOpenID}
	t.cacheReply(route.ChatID, m.ID)
	if t.flushRemainder(route, m.Content) {
		return // continue 续取:不走回合
	}
	t.flushOutbox(context.Background(), route) // 滞留内容先被动补发(新 msg_id 窗口)
	atts, note := t.mediaExtract(m.Attachments, sender)
	if note != "" {
		if text != "" {
			text += "\n" + note
		} else {
			text = note
		}
	}
	if text == "" && len(atts) == 0 {
		return
	}
	t.diagf("群消息交桥:group=%s member=%s msg=%s(未授权将在 /qq status 诊断中显示)", m.GroupOpenID, sender, m.ID)
	_ = t.bridge.HandleInbound(context.Background(), im.Inbound{Route: route, MsgID: "grp:" + m.ID, Text: text, Attachments: atts})
}

// botOpenIDValue 读取已记录的机器人自身 OpenID(READY d.user.id;未 Ready 为空)。
func (t *qqTransport) botOpenIDValue() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.botOpenID
}

// connectedWS 当前网关实际连接的 WS 地址(诊断:确认沙箱/正式;未连接为空)。
func (t *qqTransport) connectedWS() string {
	t.mu.Lock()
	gw := t.gateway
	t.mu.Unlock()
	if gw == nil {
		return ""
	}
	return gw.ConnectedURL()
}

// isSelfMessage 该群消息是否由本机器人自己发出(防对话回环)。
func (t *qqTransport) isSelfMessage(m *qqbot.GroupAtMessage) bool {
	bot := t.botOpenIDValue()
	if bot == "" {
		return false
	}
	return m.Author.ID == bot || m.Author.MemberOpenID == bot || m.Author.UserOpenID == bot
}

// mentionsBot 防御性判定群消息是否针对本机器人。
//
// 官方语义(GROUP_AT_MESSAGE_CREATE):事件类型本身即"用户 @ 机器人"的推送条件,
// 且 mentions 列表**不含 @ 机器人自身**(bot.q.qq.com 群@机器人消息 / User schema);
// 真机 @ 机器人时 mentions 常为空数组。此前实现要求 mentions 命中机器人 ID,
// 真机表现为**群内 @ 机器人完全无反应**(mentions 为空 → 恒判否 → 静默丢弃),
// 单测因 mock 自造了含机器人 ID 的 mentions 而假绿。
// 现规则:仅当能明确证明"@ 的不是本机器人"(mentions 非空且全部元素都是其它机器人)
// 才丢弃,其余一律放行(交给事件类型语义 + 桥的访问控制)。
func (t *qqTransport) mentionsBot(m *qqbot.GroupAtMessage) bool {
	if len(m.Mentions) == 0 {
		return true
	}
	bot := t.botOpenIDValue()
	for _, mt := range m.Mentions {
		if bot != "" && (mt.ID == bot || mt.MemberOpenID == bot || mt.UserOpenID == bot) {
			return true
		}
		if !mt.Bot {
			return true // 列表含真人用户:本机器人必然也是被 @ 方之一(官方语义)
		}
	}
	return false // 全部为机器人且都不是本机器人 → 其它机器人被 @,丢弃
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
	isGroup := to.Group || to.ChatID != to.UserID

	// 组装呈现清单(超限纯文本分块 / 富文本 markdown 单条 / 纯文本单条)
	msgs := buildTextMessages(text)
	// 审批确认文本 → 附加键盘(批准/拒绝按钮;action.type=2 回复消息,点击以按钮文本
	// 回消息 → 落入现有文字 y/n 管线,零新增回调协议)。键盘消息紧随说明文本。
	if strings.HasPrefix(text, confirmPrefix) {
		msgs = append(msgs, qqbot.SendMessage{MsgType: qqbot.MsgTypeKeyboard, Keyboard: confirmKeyboard()})
	}
	trunc := len(msgs) > qqMaxChunks
	if trunc {
		// 截断剩余暂存:用户回 continue/继续 时被动续发(continue 自愈)
		var rest []string
		for _, m := range msgs[qqMaxChunks:] {
			if m.Content != "" {
				rest = append(rest, m.Content)
			}
		}
		t.mu.Lock()
		if t.remainder == nil {
			t.remainder = make(map[string]string)
		}
		t.remainder[to.ChatID] = strings.Join(rest, "\n")
		t.mu.Unlock()
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

// mediaExtract 入站附件处理(P0-2c QQ 侧):图片 → 视觉附件;文本类文件 → 内容并入正文;
// 语音/视频/其它 → 落盘 + 说明。best-effort:单条失败仅记诊断,不阻断文本消息。
func (t *qqTransport) mediaExtract(atts []qqbot.MessageAttachment, who string) ([]sdk.Attachment, string) {
	if len(atts) == 0 {
		return nil, ""
	}
	t.mu.Lock()
	cli := t.client
	t.mu.Unlock()
	if cli == nil {
		return nil, ""
	}
	var out []sdk.Attachment
	var notes []string
	for i, a := range atts {
		ct := strings.ToLower(strings.TrimSpace(a.ContentType))
		name := a.FileName
		if name == "" {
			name = fmt.Sprintf("附件-%d", i+1)
		}
		if ct == "voice" { // 语音:无服务端转写文本 → 明确告知(无需下载)
			notes = append(notes, "(收到语音消息,暂不支持转写;如需可改用文字)")
			continue
		}
		if a.URL == "" {
			notes = append(notes, fmt.Sprintf("(收到 %s,但事件未给下载地址)", name))
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		data, hct, err := cli.DownloadMedia(ctx, a.URL)
		cancel()
		if err != nil {
			t.setLastError("附件下载失败: " + err.Error())
			notes = append(notes, fmt.Sprintf("(收到 %s,下载失败)", name))
			continue
		}
		ext := im.MediaExtFromContentType(ct)
		if ext == "" {
			ext = im.MediaExtFromContentType(hct)
		}
		switch {
		case strings.HasPrefix(ct, "image/") || strings.HasPrefix(strings.ToLower(hct), "image/"):
			if ext == "" {
				ext = "jpg"
			}
			p, serr := im.SaveMedia(fmt.Sprintf("%s-%d", who, i), ext, data)
			if serr != nil {
				t.setLastError("附件落盘失败: " + serr.Error())
				continue
			}
			mime := ct
			if mime == "" {
				mime = hct
			}
			out = append(out, sdk.Attachment{Kind: sdk.AttachmentImage, Name: name, MimeType: mime, Path: p})
			notes = append(notes, "(已接收图片,正在查看)")
		default: // 视频/文件
			if im.IsTextExt(ext) && len(data) <= 1<<20 {
				content := string(data)
				if r := []rune(content); len(r) > 6000 {
					content = string(r[:6000]) + "\n…(文件过长已截断)"
				}
				notes = append(notes, fmt.Sprintf("[文件 %s 内容]\n%s", name, content))
				continue
			}
			p, serr := im.SaveMedia(fmt.Sprintf("%s-%d", who, i), ext, data)
			if serr != nil {
				t.setLastError("附件落盘失败: " + serr.Error())
				continue
			}
			kind := "文件"
			if strings.HasPrefix(ct, "video/") {
				kind = "视频"
			}
			out = append(out, sdk.Attachment{Kind: sdk.AttachmentFile, Name: name, Path: p})
			notes = append(notes, fmt.Sprintf("(收到%s %s,已存 %s;如需读取请告知)", kind, name, p))
		}
	}
	return out, strings.Join(notes, "\n")
}

// flushRemainder 用户回 continue/继续 且有被截断剩余 → 被动续发(返回 true = 已消费)。
func (t *qqTransport) flushRemainder(route im.Route, text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "continue", "继续", "更多", "next", "more", "续":
	default:
		return false
	}
	t.mu.Lock()
	rest := t.remainder[route.ChatID]
	delete(t.remainder, route.ChatID)
	t.mu.Unlock()
	if rest == "" {
		return false // 无剩余:交给桥当普通消息处理(可能有意发 continue 给模型)
	}
	_ = t.SendText(context.Background(), route, rest)
	return true
}

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

// envCmd /qq env:查看/切换 OpenAPI 环境(正式 api.bot.qq.com / 沙箱 sandbox.api.sgroup.qq.com)。
func (t *qqTransport) envCmd(args []string) (string, error) {
	t.mu.Lock()
	cur := t.baseURL
	if t.creds != nil && t.creds.BaseURL != "" {
		cur = t.creds.BaseURL
	}
	t.mu.Unlock()
	if len(args) < 2 {
		return fmt.Sprintf("当前 OpenAPI 环境: %s(%s)\n切换: /qq env official|sandbox(或直接给 URL)%s",
			envName(cur), cur, t.sandboxGuide()), nil
	}
	var u string
	switch args[1] {
	case "official":
		u = qqbot.DefaultBaseURL
	case "sandbox":
		u = sandboxBaseURL
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
	if !creds.Configured() {
		return "已切换 OpenAPI 环境: " + envName(u) + "(" + u + ")(尚未配置凭证:先 /qq login)", nil
	}
	t.startGateway()
	// 即时连通性自检:换 token + GET /gateway/bot —— 失败原因直接回显(不再只落 lastError)
	if msg := t.envProbe(); msg != "" {
		return "已切换 OpenAPI 环境: " + envName(u) + "(" + u + ")。网关已重启,但**连通性自检失败**: " + msg + t.sandboxGuide(), nil
	}
	return "已切换 OpenAPI 环境: " + envName(u) + "(" + u + ")。网关已重启,连通性自检通过(token 换取 + /gateway/bot 均 OK)。" +
		"状态查询 /qq status。" + t.sandboxGuide(), nil
}

// sandboxBaseURL 沙箱环境 OpenAPI 根(官方 api-v2 文档;与正式同一 AppID/AppSecret)。
const sandboxBaseURL = "https://sandbox.api.sgroup.qq.com"

// sandboxGuide 沙箱操作指引(个人开发者群聊唯一路径:正式环境群聊需企业「公开服务」)。
func (t *qqTransport) sandboxGuide() string {
	return "\n沙箱用法(个人开发者群聊唯一路径;正式环境群聊需企业「公开服务」):\n" +
		"  1) 沙箱配置页 https://q.qq.com/qqbot/#/developer/sandbox 添加**测试用户**(你的 QQ 号)与**测试群**\n" +
		"     (测试群填 QQ **群号**(纯数字,非群 openid);须你为群主/管理员,成员 ≤20 人)\n" +
		"     ※ 看不到该页/保存失败:先完成开发者**认证**(未认证机器人仅开发者本人可用;个人认证可公开使用、进群上限 500)\n" +
		"  2) 该群里:群设置 → 群机器人 → 添加测试机器人\n" +
		"  3) 只有已配置的沙箱群/测试用户会产生事件;沙箱**不支持私聊**\n" +
		"  4) 群里 @ 机器人后 /qq status 应出现 GROUP_AT_MESSAGE_CREATE;未授权群会回配对码提示(主机侧 /im allowg <群openid>)\n" +
		"  切回正式: /qq env official"
}

// envProbe 环境连通性自检(换 token + GET /gateway/bot);返回空串 = 通过。
func (t *qqTransport) envProbe() string {
	t.mu.Lock()
	cli := t.client
	t.mu.Unlock()
	if cli == nil {
		return "网关客户端未就绪(网关未启动)"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := cli.GatewayURL(ctx); err != nil {
		return err.Error()
	}
	return ""
}

// envName 环境中文名(展示用;未识别 URL 归“自定义”)。
func envName(u string) string {
	switch {
	case strings.Contains(u, "sandbox"):
		return "sandbox(沙箱)"
	case u == qqbot.DefaultBaseURL || strings.Contains(u, "api.bot.qq.com") || strings.Contains(u, "api.sgroup.qq.com"):
		return "official(正式)"
	default:
		return "自定义"
	}
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

// —— E0/E2:IMConnectService(form 渠道路径:表单 + 即时校验 + 平台外链) ——

// ConnectSpec QQ 连接方式声明(官方无扫码鉴权 → 表单 + 外链引导;密钥不回显)。
func (a imChannelStatus) ConnectSpec() sdk.IMConnectSpec {
	tr := a.tr
	tr.mu.Lock()
	configured, appID := false, ""
	curEnv := tr.baseURL
	if tr.creds != nil {
		if tr.creds.Configured() {
			configured = true
			appID = maskTail(tr.creds.AppID)
		}
		if tr.creds.BaseURL != "" {
			curEnv = tr.creds.BaseURL
		}
	}
	tr.mu.Unlock()
	envVal := "official"
	if strings.Contains(curEnv, "sandbox") {
		envVal = "sandbox"
	}
	return sdk.IMConnectSpec{
		Channel:  tr.name,
		Kind:     sdk.IMConnectForm,
		Action:   "保存并校验",
		LoginURL: "https://q.qq.com/qqbot/#/developer/sandbox",
		DocsURL:  "https://bot.q.qq.com/wiki/",
		Hint: "QQ 官方 Bot **无扫码鉴权**(鉴权恒为 AppID+AppSecret → access_token);" +
			"需先在开放平台创建机器人并取得凭证。个人开发者的群聊需配置沙箱测试用户/测试群。",
		Fields: []sdk.IMConnectField{
			{Key: "app_id", Label: "AppID", Required: true, Placeholder: "机器人 AppID",
				Help: "开放平台 → 开发 → 开发设置", Configured: configured, Mask: appID},
			{Key: "app_secret", Label: "AppSecret", Secret: true, Required: true, Placeholder: "机器人 AppSecret",
				Help: "仅用于换取 access_token;不回显、不落日志", Configured: configured, Mask: "已配置"},
			{Key: "env", Label: "环境", Required: true, Options: []sdk.IMConnectOption{
				{Value: "official", Desc: "正式环境(bot.q.qq.com)"},
				{Value: "sandbox", Desc: "沙箱环境(个人开发者群聊必需)"},
			}, Help: "当前:" + envVal},
		},
	}
}

// StartConnect form 渠道无独立"发起"动作:返回当前状态并提示填表。
func (a imChannelStatus) StartConnect(context.Context) (sdk.IMConnectStatus, error) {
	return a.ConnectStatus(), nil
}

// SubmitConfig 提交 AppID/AppSecret/环境:落盘 → 换 token 即时校验 → 启动网关。
// 失败原因直接回显(不静默);密钥不回显(只回尾号)。
func (a imChannelStatus) SubmitConfig(ctx context.Context, values map[string]string) (sdk.IMConnectStatus, error) {
	tr := a.tr
	appID := strings.TrimSpace(values["app_id"])
	secret := strings.TrimSpace(values["app_secret"])
	env := strings.TrimSpace(values["env"])
	tr.setConnStatus(sdk.IMConnectStatus{Channel: tr.name, Phase: sdk.IMPhaseValidating, Detail: "正在校验凭证…"})
	fail := func(msg string) (sdk.IMConnectStatus, error) {
		st := sdk.IMConnectStatus{Channel: tr.name, Phase: sdk.IMPhaseFailed, Error: msg}
		tr.setConnStatus(st)
		return st, fmt.Errorf("%s", msg)
	}
	if appID == "" || secret == "" {
		return fail("AppID 与 AppSecret 均不能为空(开放平台 → 开发 → 开发设置)")
	}
	// 密钥未重填(哨兵/空)→ 沿用已配置值;环境显式选择则先落盘(校验失败也保留用户选择)
	tr.mu.Lock()
	if tr.creds == nil {
		tr.creds = &qqbot.Credentials{}
	}
	if secret == maskedSentinel {
		secret = tr.creds.AppSecret
	}
	if secret == "" {
		tr.mu.Unlock()
		return fail("AppSecret 为必填(未配置过请填写;已配置可留空沿用)")
	}
	switch env {
	case "official":
		tr.creds.BaseURL = qqbot.DefaultBaseURL
	case "sandbox":
		tr.creds.BaseURL = sandboxBaseURL
	}
	tr.mu.Unlock()
	// login:落盘凭证 + 启动网关 + 换 token 即时校验(失败原因含排查指引)
	if err := tr.login(appID, secret); err != nil {
		return fail(err.Error())
	}
	st := sdk.IMConnectStatus{Channel: tr.name, Phase: sdk.IMPhaseDone,
		Detail: "凭证已保存并校验通过", Account: maskTail(appID)}
	if env == "sandbox" {
		st.Env = "sandbox"
	}
	tr.setConnStatus(st)
	return st, nil
}

// ConnectStatus 当前连接状态(读 status 聚合 + 最近一次连接相位)。
func (a imChannelStatus) ConnectStatus() sdk.IMConnectStatus {
	tr := a.tr
	tr.mu.Lock()
	conn := tr.conn
	online := false
	if tr.creds != nil && tr.creds.Configured() {
		online = true
	}
	appID := ""
	if tr.creds != nil {
		appID = maskTail(tr.creds.AppID)
	}
	tr.mu.Unlock()
	if conn.Phase == sdk.IMPhaseValidating || conn.Phase == sdk.IMPhaseFailed {
		return conn
	}
	if online {
		st := sdk.IMConnectStatus{Channel: tr.name, Phase: sdk.IMPhaseDone, Detail: "已配置", Account: appID}
		if tr.gatewayOnline() {
			st.Detail = "网关在线"
		}
		return st
	}
	return sdk.IMConnectStatus{Channel: tr.name, Phase: sdk.IMPhaseIdle, Detail: "未配置(需 AppID/AppSecret)"}
}

// setConnStatus 写连接状态并广播事件(im/connect)。
func (t *qqTransport) setConnStatus(st sdk.IMConnectStatus) {
	t.mu.Lock()
	t.conn = st
	b := t.bridge
	t.mu.Unlock()
	if b != nil {
		b.EmitConnect(st)
	}
}

// gatewayOnline 网关是否在线(简化只读判定)。
func (t *qqTransport) gatewayOnline() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.gateway != nil && t.gateway.Online()
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

// Groups sdk.IMGroupAccessService 转发(G-E5-2:Web 面板「群授权」区段)。
func (a imChannelStatus) Groups() []sdk.IMGroupEntry {
	if a.bridge == nil {
		return nil
	}
	return a.bridge.Groups()
}

// SetGroupAccess sdk.IMGroupAccessService 转发(授权/撤销一个群;未知群撤销显式报错)。
func (a imChannelStatus) SetGroupAccess(chatID string, allow bool) error {
	if a.bridge == nil {
		return fmt.Errorf("im: 桥未装配")
	}
	return a.bridge.SetGroupAccess(chatID, allow)
}

// Disconnect sdk.IMDisconnectProvider 转发(E3-R /im logout)。
func (a imChannelStatus) Disconnect(context.Context) error { return a.tr.disconnect() }

// disconnect E3-R:停止网关并清理本地凭证(AppID/AppSecret;授权/群名单保留),幂等。
func (t *qqTransport) disconnect() error {
	t.stopGateway()
	t.mu.Lock()
	if t.creds != nil {
		t.creds.AppID = ""
		t.creds.AppSecret = ""
	}
	creds, store := t.creds, t.store
	t.mu.Unlock()
	if creds != nil && store != nil {
		if err := store.Save(creds); err != nil {
			return err
		}
	}
	t.setLastError("未配置(执行 /qq login)")
	t.setConnStatus(sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseIdle, Detail: "已退出登录"})
	return nil
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
	groups, mode := 0, ""
	if t.bridge != nil {
		groups = len(t.bridge.Access().Groups())
		mode = string(t.bridge.Access().Mode())
	}
	if t.creds != nil && t.creds.BaseURL != "" {
		cfg += "@" + t.creds.BaseURL
	}
	curEnv := t.baseURL
	if t.creds != nil && t.creds.BaseURL != "" {
		curEnv = t.creds.BaseURL
	}
	tokenURL := t.tokenURL
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
	out := fmt.Sprintf("qq: %s(%s) 环境=%s 网关=%s 访问=%s 已授权用户=%d 已授权群=%d\n最近: %s",
		cfg, appID, envName(curEnv), gw, mode, allowed, groups, lastErr)
	out += fmt.Sprintf("\n事件统计: C2C=%d 群@=%d 其它=%d(群@ 长期为 0 = 平台侧未推,查沙箱配置/认证)",
		t.evCount("c2c"), t.evCount("group"), t.evCount("other"))
	out += "\ntoken 端点: " + tokenURL
	if strings.Contains(curEnv, "sandbox") {
		out += "\n(沙箱:仅已在「沙箱配置」中添加的群/测试用户会产生事件;沙箱不支持私聊)"
	}
	if d := t.diagLines(8); d != "" {
		out += "\n诊断(最近 8 条;设 GAH_QQ_DEBUG=1 可同步打 stderr):\n" + d
	}
	return out
}

// mask 凭证掩码(前 4 位 + 长度,防全量泄露)。
func mask(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:4] + "…" + fmt.Sprintf("(%d)", len(s))
}

// maskTail 脱敏摘要(只留尾号 4 位;短值原样)。与微信壳同名同义(两端各自持有,避免跨插件依赖)。
func maskTail(s string) string {
	if len(s) <= 4 {
		return s
	}
	return "…" + s[len(s)-4:]
}
