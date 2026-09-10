// Package uimwechat 提供 ui-im-wechat 插件(IM 远程控制线 P0-2b):微信个人号经 iLink Bot API
// 接入 im.Bridge——transport(poll loop:getupdates 长轮询 → im.HandleInbound;SendText → sendmessage,
// context_token 按用户缓存回显) + 装配(注入 loop/sessions → im.New → Provide ctx.confirm +
// 注册 /stop、/im(桥)+ /wechat login|status(通道命令))。凭证入 $GAH_HOME/config/ilink-wechat.yaml。
// 安全:默认 pairing(登录成功自动授权扫码者);allowlist 经 store 持久。媒体 CDN 留 P0-2c。
package uimwechat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/nekoleamo/go-agent-harness/ilink"
	"github.com/nekoleamo/go-agent-harness/im"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 ui-im-wechat。Requires ctx.agentLoop/ctx.sessions(注入);
// 提供 ctx.confirm(IM 审批通道,与 tui/web profile 互斥——profile 层保证)。
type Plugin struct{}

func (p *Plugin) Name() string { return "ui-im-wechat" }

const channelName = "wechat"

// 出站长回复策略:iLink 单条兼容上限 ~2000 字(社区保守值,非服务端公开);
// 短窗口连发受限(社区实测 ~10 条/窗口,超出尾部静默丢失,用户发消息才能恢复)。
const (
	wechatChunkLimit = 2000                   // 单条消息上限(按 Unicode 字符)
	wechatChunkGap   = 300 * time.Millisecond // 分块间隔(防连发触发窗口截断)
	wechatMaxChunks  = 10                     // 单回合最多分块数(超出截断 + 提示)
)

// Start 装配桥与通道;已登录则自动启动 poll loop;未登录(data.auto_login 默认 true)
// 自动发起扫码登录(二维码链接打印 stderr,headless 场景无交互入口;确认后自动授权并启动)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	mode := im.AccessPairing
	baseURL := ilink.DefaultBaseURL
	autoLogin := true
	autoRelogin := true // 会话过期自动重登(默认开;关闭后仅提示,由用户手动 /wechat login)
	if m != nil && m.Data != nil {
		if v, ok := m.Data["mode"].(string); ok && v != "" {
			mode = im.AccessMode(v)
		}
		if v, ok := m.Data["base_url"].(string); ok && v != "" {
			baseURL = v
		}
		if v, ok := m.Data["auto_login"].(bool); ok {
			autoLogin = v
		}
		if v, ok := m.Data["auto_relogin"].(bool); ok {
			autoRelogin = v
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

	store := ilink.NewStore(credsPath())
	creds, err := store.Load()
	if err != nil {
		return nil, err
	}
	tr := &wechatTransport{name: channelName, store: store, creds: creds, baseURL: baseURL,
		lastError: "未登录(执行 /wechat login)", tickets: make(map[string]ticketEntry),
		sender:    im.NewSender(&im.Budget{MaxChunk: wechatChunkLimit, MaxChunks: wechatMaxChunks, Gap: wechatChunkGap}),
		remainder: make(map[string]string), autoRelogin: autoRelogin}
	b := im.New(c, loop, sessions, tr, im.Options{
		Mode:        mode,
		Allow:       creds.Allow,  // 已授权用户持久恢复
		AllowGroups: creds.Groups, // 已授权群持久恢复(群维度授权)
		// P1 会话绑定:chat→宿主会话映射落盘(重启恢复绑定)
		SessionBindPath: sessionBindPath(),
	})
	tr.bridge = b
	// 授权变化持久化(/im pair 批准、allow/revoke、登录授权):写回凭证 store,
	// 重启恢复——配对批准不再因重启丢失(P1 真机需求)。回调锁外触发,List 安全。
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
	// 已登录自动启动 poll loop
	if creds.Token != "" {
		tr.client = ilink.New(creds.BaseURL, creds.Token)
		tr.startPoll()
	} else if autoLogin {
		tr.setLastError("未登录,自动发起扫码登录…")
		go tr.autoLogin()
	}
	// ctx.imChannels:Web/桌面设置面板 IM 通道状态 + 扫码登录(P3 三端融合;
	// 适配器同时实现 sdk.IMLoginProvider → web /api/im/login 可用)
		// G-E5-3:受控出站面(im_send/im_status 经此投递;工具默认不注册,见 plugins/tool/tool-im)
	if err := c.Provide("ctx.imControl", b); err != nil {
		return nil, err
	}
if err := c.Provide("ctx.imChannels", imChannelStatus{tr: tr, bridge: b}); err != nil {
		return nil, err
	}
	// ctx.confirm = IM 桥(单 profile 自提供;P3 融合:装配 host-confirm-fusion 时
	// 注册呈现者与 web 并存,不 Provide 防同名冲突)
	var confirmReg sdk.Disposer = func() {}
	var questionReg sdk.Disposer = func() {}
	var fusion sdk.ConfirmFusion
	if err := c.Inject("ctx.confirmFusion", &fusion); err == nil && fusion != nil {
		confirmReg = fusion.Register("im-wechat", b)
		// P3 语义交互:同一桥作为提问呈现者注册(与确认同管道,首答生效)
		var qfusion sdk.QuestionService
		if err := c.Inject("ctx.question", &qfusion); err == nil && qfusion != nil {
			questionReg = qfusion.RegisterQuestioner("im-wechat", b)
		}
	} else {
		if err := c.Provide("ctx.confirm", b); err != nil {
			return nil, err
		}
		// 单 profile(无 fusion):桥自身提供结构化提问服务
		// G-E5-4:包装 ObservableQuestion → 单 profile 也广播 question/requested ↔ resolved
		if err := c.Provide("ctx.question", sdk.ObservedQuestion(c, "im-wechat", im.NewQuestionService(b))); err != nil {
			return nil, err
		}
	}
	// 命令注册:桥自带 /stop /im(pair/status/list)+ 通道命令 /wechat login|status
	var ds []sdk.Disposer
	if cmds != nil {
		d, err := b.RegisterCommands(cmds)
		if err != nil {
			return nil, err
		}
		ds = append(ds, d)
		d2, err := cmds.Register(sdk.CommandSpec{
			Name:  "wechat",
			Usage: "/wechat login|status",
			Desc:  "微信 iLink 通道:扫码登录/状态",
			Run:   func(args []string) (string, error) { return tr.wechatCmd(context.Background(), args) },
			// 二级选项(status 查看 / login 扫码登录或重新登录;login 无额外参数)
			Args: []sdk.ArgLevel{{Options: func([]string) []sdk.Option {
				return []sdk.Option{
					{Value: "status", Desc: "查看登录态(账号/token 尾号/轮询/已授权)"},
					{Value: "login", Desc: "扫码登录(已登录则重新扫码并覆盖凭证)"},
				}
			}}},
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
	ds = append(ds, b.WatchInteraction(c, "im-wechat"))
	return func() {
		confirmReg()
		questionReg()
		tr.stopPoll()
		for _, d := range ds {
			d()
		}
	}, nil
}

// credsPath 凭证路径:$GAH_HOME/config/ilink-wechat.yaml(便携纪律;空 GAH_HOME 兜底 TempDir)。
func credsPath() string {
	home := os.Getenv("GAH_HOME")
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, "config", "ilink-wechat.yaml")
}

// sessionBindPath chat↔宿主会话绑定映射路径:$GAH_HOME/config/im-sessions.yaml
// (P1 会话绑定命令面;与 QQ 线同根文件——同进程并存时共享绑定表)。
func sessionBindPath() string {
	home := os.Getenv("GAH_HOME")
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, "config", "im-sessions.yaml")
}

// imChannelStatus sdk.IMChannelService 适配(web 面板 IM 通道状态)。
type imChannelStatus struct {
	tr     *wechatTransport
	bridge *im.Bridge
}

// Status 聚合渠道状态(state:online>running>configuring>offline)。
func (a imChannelStatus) Status() []sdk.IMChannelStatus {
	tr := a.tr
	tr.mu.Lock()
	state := "offline"
	if tr.polling {
		state = "online"
		if tr.creds == nil || tr.creds.Token == "" {
			state = "configuring"
		}
	}
	if tr.creds == nil || tr.creds.Token == "" {
		state = "configuring"
	}
	lastErr := tr.lastError
	tr.mu.Unlock()
	detail := tr.statusText()
	auth := 0
	if a.bridge != nil {
		auth = len(a.bridge.Access().List())
	}
	return []sdk.IMChannelStatus{{
		Channel: tr.name, State: state, Detail: detail, Error: lastErr, Authorized: auth,
	}}
}

// StartLogin sdk.IMLoginProvider 转发(web 面板经 ctx.imChannels 类型断言发现)。
func (a imChannelStatus) StartLogin(ctx context.Context) (sdk.IMLoginQR, error) {
	return a.tr.StartLogin(ctx)
}

// LoginState sdk.IMLoginProvider 转发。
func (a imChannelStatus) LoginState() sdk.IMLoginState { return a.tr.LoginState() }

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

// ticketEntry typing 票据缓存。
type ticketEntry struct {
	ticket    string
	expiresAt time.Time
}

// wechatTransport 实现 im.Transport + poll loop。
type wechatTransport struct {
	name    string
	store   *ilink.Store
	creds   *ilink.Credentials
	baseURL string
	bridge  *im.Bridge
	client  *ilink.Client
	sender  *im.Sender // 出站预算层(P1b):统一分块/截断/间隔

	mu          sync.Mutex
	polling     bool
	stopCh      chan struct{}
	lastError   string
	tickets     map[string]ticketEntry
	tokens      map[string]string // userID → context_token(iLink 回显必须)
	loginBusy   bool
	remainder   map[string]string   // chatID → 被截断的剩余文本(用户回 continue 时被动补发)
	loginState  sdk.IMLoginState    // 面板扫码登录进度(P3 Web 面板;兼容旧端点)
	conn        sdk.IMConnectStatus // E0/E1:连接卡相位 + 当前二维码(过期自动重取时刷新)
	autoRelogin bool                // 会话过期时自动清理失效凭证并发起重新扫码(data.auto_relogin,默认 true)
	typingCtl   context.CancelFunc  // 回合进行中的 typing 周期刷新控制器(回合结束取消)
}

func (t *wechatTransport) Name() string { return t.name }

// ShowTyping im.TypingAware:回合开始显示“正在输入”并周期刷新(iLink typing 状态生命周期
// 短,每 ~20s 重发 show 保持);回合结束由 StopTyping 取消。best-effort(失败静默)。
func (t *wechatTransport) ShowTyping(_ context.Context, r im.Route) error {
	user := r.UserID
	t.mu.Lock()
	if t.typingCtl != nil {
		t.typingCtl() // 上一个回合残留刷新先停
	}
	c2, cancel := context.WithCancel(context.Background())
	t.typingCtl = cancel
	t.mu.Unlock()
	go func() {
		t.sendTypingNow(user, true)
		tk := time.NewTicker(5 * time.Second) // iLink typing 生命周期短,~5s keepalive 保持
		defer tk.Stop()
		for {
			select {
			case <-c2.Done():
				return
			case <-tk.C:
				t.sendTypingNow(user, true)
			}
		}
	}()
	return nil
}

// StopTyping im.TypingAware:回合结束取消 typing 并即时发 cancel。
func (t *wechatTransport) StopTyping(_ context.Context, r im.Route) error {
	t.mu.Lock()
	c := t.typingCtl
	t.typingCtl = nil
	t.mu.Unlock()
	if c != nil {
		c()
	}
	t.sendTypingNow(r.UserID, false)
	return nil
}

// sendTypingNow 发送一次 typing 状态(show=true 显示输入中;失败静默——typing 尽力而为)。
func (t *wechatTransport) sendTypingNow(user string, show bool) {
	t.mu.Lock()
	cli := t.client
	t.mu.Unlock()
	if cli == nil {
		return
	}
	if tkt := t.typingTicket(user); tkt != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = cli.SendTyping(ctx, user, tkt, show)
	}
}

// SendText 回推文本(context_token 缺失无法发送,记诊断)。
func (t *wechatTransport) SendText(ctx context.Context, to im.Route, text string) error {
	if to.Group {
		return fmt.Errorf("wechat: 该渠道不支持群投递(iLink 为单聊会话)")
	}
	t.mu.Lock()
	cli := t.client
	token := t.tokens[to.UserID]
	user := to.UserID
	t.mu.Unlock()
	if cli == nil {
		return fmt.Errorf("wechat: 未登录")
	}
	if token == "" {
		return fmt.Errorf("wechat: 无 %s 的 context_token,无法回复(请对方先发消息)", user)
	}
	// 出站预算层(im.Sender):rune 安全分块(单条 ~2000)+ 一轮 ≤10 块截断提示 +
	// 块间间隔防连发触发短窗口截断(社区头号坑:hermes/cc-connect 长回复尾部静默丢失)。
	// 截断剩余暂存;用户回 continue/继续 时被动补发(continue 自愈)。
	rest, err := t.sender.SendSplit(ctx, text, func(chunk string) error {
		return cli.SendMessage(ctx, to.UserID, chunk, token, "")
	})
	if rest != "" {
		t.mu.Lock()
		if t.remainder == nil {
			t.remainder = make(map[string]string)
		}
		t.remainder[to.UserID] = rest
		t.mu.Unlock()
	}
	return err
}

// typingTicket 取(缓存 ~20h;失败静默——typing 尽力而为)。
func (t *wechatTransport) typingTicket(user string) string {
	t.mu.Lock()
	if e, ok := t.tickets[user]; ok && time.Now().Before(e.expiresAt) {
		t.mu.Unlock()
		return e.ticket
	}
	t.mu.Unlock()
	cli := t.client
	if cli == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tkt, err := cli.GetTypingTicket(ctx, user)
	if err != nil {
		return ""
	}
	t.mu.Lock()
	t.tickets[user] = ticketEntry{ticket: tkt, expiresAt: time.Now().Add(20 * time.Hour)}
	t.mu.Unlock()
	return tkt
}

// startPoll 启动轮询 goroutine(幂等)。
func (t *wechatTransport) startPoll() {
	t.mu.Lock()
	if t.polling {
		t.mu.Unlock()
		return
	}
	t.polling = true
	t.stopCh = make(chan struct{})
	stop := t.stopCh
	t.mu.Unlock()
	go t.pollLoop(stop)
}

// stopPoll 停止轮询(幂等)。
func (t *wechatTransport) stopPoll() {
	t.mu.Lock()
	if !t.polling {
		t.mu.Unlock()
		return
	}
	t.polling = false
	close(t.stopCh)
	t.mu.Unlock()
}

// pollLoop 长轮询主循环:取消息 → 桥处理;断线退避;会话过期(errcode -14)停轮询并置诊断。
func (t *wechatTransport) pollLoop(stop chan struct{}) {
	backoff := 2 * time.Second
	for {
		select {
		case <-stop:
			return
		default:
		}
		t.mu.Lock()
		creds := *t.creds
		cli := t.client
		t.mu.Unlock()
		if cli == nil {
			time.Sleep(2 * time.Second)
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { // stop 时取消本轮长轮询(否则最长挂 35s)
			select {
			case <-stop:
				cancel()
			case <-done:
			}
		}()
		resp, err := cli.GetUpdates(ctx, creds.SyncBuf)
		close(done)
		if err != nil {
			cancel()
			t.setLastError("轮询错误: " + err.Error())
			if ilink.SessionExpired(err) {
				t.invalidateCreds() // 失效凭证清理落盘(避免重启后继续失败)
				if t.autoRelogin {
					t.setLastError("微信会话已过期(ret=-14):已清理失效凭证并自动发起重新登录;请扫码(终端二维码 / Web 面板「扫码登录」)")
					go t.autoLogin() // 取新二维码(打印到 stderr)+ 后台等待确认
				} else {
					t.setLastError("微信会话已过期(ret=-14),请重新执行 /wechat login")
				}
				return // 停轮询,等重新登录
			}
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 2 * time.Second
		t.setLastError("")
		if resp.GetUpdatesBuf != "" && resp.GetUpdatesBuf != creds.SyncBuf {
			t.mu.Lock()
			t.creds.SyncBuf = resp.GetUpdatesBuf
			_ = t.store.Save(t.creds)
			t.mu.Unlock()
		}
		for _, msg := range resp.Msgs {
			// 每条消息独立 goroutine 处理:回合(含 Confirm 等待)若占住轮询线程,
			// 将不再 getupdates,用户在确认等待期间回复的 y/n 永远拉不进来 → 必超时
			// (真机反馈:y、n 均 2 分钟超时;mock 因测试多 goroutine 并发才通过)。
			// 并发安全性由 im.Bridge 保证(busy 互斥 + pending 先判定);panic 隔离在
			// handleInbound 内 recover,避免单条消息拖死整个轮询。
			m := msg
			go t.handleInbound(&m)
		}
	}
}

// handleInbound 解析一条入站消息交给桥(token 缓存 + 去重 id 由桥负责)。
// 由独立 goroutine 调用;panic 隔离(单条消息异常不拖死轮询/进程)。
func (t *wechatTransport) handleInbound(msg *ilink.InboundMessage) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[wechat] 消息处理 panic(已隔离): %v\n", r)
		}
	}()
	if msg.MessageType != 1 || msg.FromUserID == "" {
		return
	}
	// continue 自愈:用户回 continue/继续 且存在被截断剩余 → 直接被动补发(不走回合)
	if isContinueText(msg.ExtractText()) {
		if rest := t.takeRemainder(msg.FromUserID); rest != "" {
			route := im.Route{Channel: t.name, UserID: msg.FromUserID, ChatID: msg.FromUserID}
			t.mu.Lock()
			if msg.ContextToken != "" {
				if t.tokens == nil {
					t.tokens = make(map[string]string)
				}
				t.tokens[msg.FromUserID] = msg.ContextToken
			}
			t.mu.Unlock()
			_ = t.SendText(context.Background(), route, rest)
			return
		}
	}
	// 媒体入站(P0-2c):图片/文本文件预下载(CDN token 有有效期,即时取)并解密;
	// 图片走附件视觉注入,文本文件内容并入正文,其它文件落盘 + 说明。
	atts, mediaNote := t.mediaExtract(msg)
	text := msg.ExtractText()
	if mediaNote != "" {
		if text != "" {
			text += "\n" + mediaNote
		} else {
			text = mediaNote
		}
	}
	if text == "" && len(atts) == 0 {
		return
	}
	t.mu.Lock()
	if t.tokens == nil {
		t.tokens = make(map[string]string)
	}
	if msg.ContextToken != "" {
		t.tokens[msg.FromUserID] = msg.ContextToken
	}
	t.mu.Unlock()
	route := im.Route{Channel: t.name, UserID: msg.FromUserID, ChatID: msg.FromUserID}
	// 稳定消息 id:ts + 文本指纹(重复投递去重;跨渠道无需全局)
	h := sha256.Sum256([]byte(text))
	msgID := fmt.Sprintf("%d-%s", msg.CreateTimeMs, hex.EncodeToString(h[:6]))
	_ = t.bridge.HandleInbound(context.Background(), im.Inbound{Route: route, MsgID: msgID, Text: text, Attachments: atts})
}

// StartLogin sdk.IMLoginProvider:面板发起扫码登录(等价 /wechat login 的二维码流程)。
func (t *wechatTransport) StartLogin(ctx context.Context) (sdk.IMLoginQR, error) {
	t.mu.Lock()
	if t.loginBusy {
		st := t.loginState
		t.mu.Unlock()
		return sdk.IMLoginQR{}, fmt.Errorf("登录进行中(%s);若二维码已过期请稍候重试", st.Detail)
	}
	t.mu.Unlock()
	qr, err := ilink.FetchQR(ctx, t.baseURL)
	if err != nil {
		t.setLoginState(sdk.IMLoginState{Phase: "failed", Error: err.Error()})
		return sdk.IMLoginQR{}, fmt.Errorf("获取二维码失败: %w", err)
	}
	t.mu.Lock()
	t.loginBusy = true
	t.loginState = sdk.IMLoginState{Phase: "pending", Detail: "二维码已生成,请用微信扫码并在手机确认(5 分钟内)"}
	t.mu.Unlock()
	go func() {
		defer func() {
			t.mu.Lock()
			t.loginBusy = false
			t.mu.Unlock()
		}()
		if err := t.finishLogin(qr); err != nil {
			t.setLoginState(sdk.IMLoginState{Phase: "failed", Error: err.Error()})
			return
		}
		t.setLoginState(sdk.IMLoginState{Phase: "done", Detail: "登录成功"})
	}()
	return sdk.IMLoginQR{Channel: t.name, Content: qr.QRCodeImg, ExpiresAt: time.Now().Add(5 * time.Minute)}, nil
}

// LoginState sdk.IMLoginProvider:当前登录进度(无进行中登录则按已登录态返回 idle/done)。
func (t *wechatTransport) LoginState() sdk.IMLoginState {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.loginBusy {
		return t.loginState
	}
	if t.creds != nil && t.creds.Token != "" {
		return sdk.IMLoginState{Phase: "done", Detail: "已登录(如需换号请重新扫码)"}
	}
	if t.loginState.Phase == "failed" {
		return t.loginState
	}
	return sdk.IMLoginState{Phase: "idle", Detail: "未登录"}
}

// —— E0/E1:IMConnectService(qr 渠道路径) ——

// ConnectSpec 微信连接方式声明(扫码;协议不下发有效期 → 提示过期自动刷新)。
func (a imChannelStatus) ConnectSpec() sdk.IMConnectSpec {
	return sdk.IMConnectSpec{
		Channel: a.tr.name,
		Kind:    sdk.IMConnectQR,
		Action:  "扫码登录",
		Hint:    "用微信扫码并在手机上确认;协议不下发有效期,过期会自动刷新二维码,无需重按",
		DocsURL: "https://github.com/nekoleamo/go-agent-harness",
	}
}

// StartConnect 发起扫码登录(内部经 LoginQRWithProgress:相位细粒度 + 过期自动重取)。
func (a imChannelStatus) StartConnect(ctx context.Context) (sdk.IMConnectStatus, error) {
	return a.tr.StartConnect(ctx)
}

// SubmitConfig 微信无表单配置(扫码登录);显式拒绝而非假成功。
func (a imChannelStatus) SubmitConfig(context.Context, map[string]string) (sdk.IMConnectStatus, error) {
	return sdk.IMConnectStatus{Channel: a.tr.name, Phase: sdk.IMPhaseFailed,
		Error: "微信为扫码登录,无表单配置项"}, fmt.Errorf("微信为扫码登录,无表单配置项")
}

// ConnectStatus 当前连接状态(读同一状态机)。
func (a imChannelStatus) ConnectStatus() sdk.IMConnectStatus { return a.tr.ConnectStatus() }

// StartConnect 连接卡启动:取码 → 相位回调(事件化)→ 成功后保存凭证并启动轮询。
// 与 StartLogin 的差别:① 相位更细(含 expired_refresh);② 过期自动重取(新码同步到 conn);
// ③ 相位变化 emit im/connect(各端订阅,不再 2s 高频轮询)。
func (t *wechatTransport) StartConnect(ctx context.Context) (sdk.IMConnectStatus, error) {
	t.mu.Lock()
	if t.loginBusy {
		st := t.conn
		t.mu.Unlock()
		return st, fmt.Errorf("登录进行中(%s)", st.Detail)
	}
	t.loginBusy = true
	t.conn = sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseWaitingScan, Detail: "正在获取二维码…"}
	t.mu.Unlock()
	// 立刻返回"取码中":取码在后台完成并通过事件推送二维码(接口不阻塞等待扫码)
	go func() {
		defer func() {
			t.mu.Lock()
			t.loginBusy = false
			t.mu.Unlock()
		}()
		creds, err := ilink.LoginQRWithProgress(ctx, t.baseURL, 10*time.Minute,
			func(qr *ilink.QRResponse) {
				t.setConn(sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseWaitingScan,
					Detail: "请用微信扫码并在手机上确认", QRContent: qr.QRCodeImg})
			},
			func(phase, detail string) {
				if phase == "validating" {
					t.setConnPhase(sdk.IMPhaseValidating, detail)
					return
				}
				t.setConnPhase(phase, detail)
			})
		if err != nil {
			t.setConn(sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseFailed, Error: err.Error()})
			t.setLoginState(sdk.IMLoginState{Phase: "failed", Error: err.Error()})
			return
		}
		if err := t.applyCreds(creds); err != nil {
			t.setConn(sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseFailed, Error: err.Error()})
			t.setLoginState(sdk.IMLoginState{Phase: "failed", Error: err.Error()})
			return
		}
		t.setConn(sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseDone, Detail: "登录成功",
			Account: maskTail(creds.AccountID)})
		t.setLoginState(sdk.IMLoginState{Phase: "done", Detail: "登录成功"})
	}()
	return sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseWaitingScan, Detail: "正在获取二维码…"}, nil
}

// applyCreds 保存凭证 + 授权扫码者 + 启动轮询(finishLogin 的复用内核)。
func (t *wechatTransport) applyCreds(creds *ilink.Credentials) error {
	t.mu.Lock()
	if !hasStr(t.creds.Allow, channelName+"\x00"+creds.UserID) && creds.UserID != "" {
		t.creds.Allow = append(t.creds.Allow, channelName+"\x00"+creds.UserID)
	}
	t.creds.Token = creds.Token
	t.creds.BaseURL = creds.BaseURL
	t.creds.AccountID = creds.AccountID
	t.creds.UserID = creds.UserID
	t.creds.SyncBuf = ""
	if err := t.store.Save(t.creds); err != nil {
		t.mu.Unlock()
		t.setLastError("保存凭证失败: " + err.Error())
		return err
	}
	t.client = ilink.New(t.creds.BaseURL, t.creds.Token)
	t.mu.Unlock()
	t.bridge.Access().Allow(channelName + "\x00" + creds.UserID)
	t.setLastError("")
	t.startPoll()
	return nil
}

// ConnectStatus 当前连接状态(登录中读 conn;已登录 → done)。
func (t *wechatTransport) ConnectStatus() sdk.IMConnectStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.loginBusy {
		return t.conn
	}
	if t.creds != nil && t.creds.Token != "" {
		return sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseDone, Detail: "已登录",
			Account: maskTail(t.creds.AccountID)}
	}
	if t.conn.Phase == sdk.IMPhaseFailed {
		return t.conn
	}
	return sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseIdle, Detail: "未登录"}
}

// setConn 写连接状态并广播事件(事件化替代轮询)。
func (t *wechatTransport) setConn(st sdk.IMConnectStatus) {
	t.mu.Lock()
	// 保留已取到的二维码(仅扫码进行中的相位;idle/done/failed 等终态一律清空)
	switch st.Phase {
	case sdk.IMPhaseWaitingScan, sdk.IMPhaseScanned, sdk.IMPhaseExpiredRefresh, sdk.IMPhaseValidating:
		if st.QRContent == "" {
			st.QRContent = t.conn.QRContent
		}
	}
	t.conn = st
	t.mu.Unlock()
	if t.bridge != nil {
		t.bridge.EmitConnect(st)
	}
}

// setConnPhase 仅推进相位(保留二维码)。
func (t *wechatTransport) setConnPhase(phase, detail string) {
	t.mu.Lock()
	st := t.conn
	st.Phase, st.Detail = phase, detail
	if phase == sdk.IMPhaseExpiredRefresh {
		st.QRContent = "" // 旧码作废,等新码回调填入
	}
	t.conn = st
	t.mu.Unlock()
	if t.bridge != nil {
		t.bridge.EmitConnect(st)
	}
}

// maskTail 脱敏摘要(只留尾号 4 位;空值原样)。
func maskTail(s string) string {
	if len(s) <= 4 {
		return s
	}
	return "…" + s[len(s)-4:]
}

// setLoginState 写登录进度。
func (t *wechatTransport) setLoginState(st sdk.IMLoginState) {
	t.mu.Lock()
	t.loginState = st
	t.mu.Unlock()
}

// takeRemainder 取走并清空指定会话的截断剩余(continue 补发用)。
func (t *wechatTransport) takeRemainder(userID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	rest := t.remainder[userID]
	delete(t.remainder, userID)
	return rest
}

// isContinueText 是否"继续/续取"指令(截断剩余补发触发词;大小写不敏感)。
func isContinueText(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "continue", "继续", "更多", "next", "more", "续":
		return true
	}
	return false
}

// mediaExtract 媒体入站提取:下载+解密媒体项 → 附件(图片视觉)与正文说明(文本文件内容)。
// best-effort:单条媒体失败仅记诊断并跳过,不阻断整条文本消息(防 CDN/密钥异常拖垮对话)。
func (t *wechatTransport) mediaExtract(msg *ilink.InboundMessage) ([]sdk.Attachment, string) {
	var atts []sdk.Attachment
	var notes []string
	for i, it := range msg.ItemList {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		switch it.Type {
		case 2: // 图片 → 视觉附件
			if it.ImageItem == nil {
				cancel()
				continue
			}
			url := it.ImageItem.URL
			aesKey := it.ImageItem.AesKey
			if url == "" && it.ImageItem.Media != nil {
				url = it.ImageItem.Media.FullURL
				if aesKey == "" {
					aesKey = it.ImageItem.Media.AesKey
				}
			}
			if url == "" {
				cancel()
				continue
			}
			data, derr := ilink.DownloadMedia(ctx, url)
			if derr == nil && aesKey != "" {
				data, _ = ilink.DecryptMedia(data, aesKey) // 解密失败宽容保留原文
			}
			if derr != nil {
				t.setLastError("媒体下载失败: " + derr.Error())
				cancel()
				continue
			}
			ext := ilink.ExtFromURL(url)
			if ext == "" {
				ext = "jpg"
			}
			path, serr := im.SaveMedia(fmt.Sprintf("%s-%d", msg.FromUserID, i), ext, data)
			cancel()
			if serr != nil {
				t.setLastError("媒体落盘失败: " + serr.Error())
				continue
			}
			atts = append(atts, sdk.Attachment{Kind: sdk.AttachmentImage, Name: "图片" + ext, MimeType: "image/" + ext, Path: path})
			notes = append(notes, fmt.Sprintf("(已接收 %d 张图片,正在查看)", 1))
		case 4: // 文件 → 文本类提取内容入正文;其它落盘+路径引用
			if it.FileItem == nil {
				cancel()
				continue
			}
			name := it.FileItem.FileName
			if it.FileItem.Media == nil || it.FileItem.Media.FullURL == "" {
				cancel()
				continue
			}
			data, derr := ilink.DownloadMedia(ctx, it.FileItem.Media.FullURL)
			if derr == nil && it.FileItem.Media.AesKey != "" {
				data, _ = ilink.DecryptMedia(data, it.FileItem.Media.AesKey)
			}
			if derr != nil {
				t.setLastError("文件下载失败: " + derr.Error())
				cancel()
				continue
			}
			cancel()
			ext := ilink.ExtFromURL(it.FileItem.Media.FullURL)
			if ext == "" {
				ext = "bin"
			}
			if im.IsTextExt(ext) && len(data) <= 1<<20 { // 文本类 ≤1MB → 内容并入正文(截断防护)
				content := string(data)
				if r := []rune(content); len(r) > 6000 {
					content = string(r[:6000]) + "\n…(文件过长已截断)"
				}
				if name == "" {
					name = "附件." + ext
				}
				notes = append(notes, fmt.Sprintf("[文件 %s 内容]\n%s", name, content))
			} else {
				path, serr := im.SaveMedia(fmt.Sprintf("%s-%d", msg.FromUserID, i), ext, data)
				if serr != nil {
					t.setLastError("文件落盘失败: " + serr.Error())
					continue
				}
				atts = append(atts, sdk.Attachment{Kind: sdk.AttachmentFile, Name: name, Path: path})
				notes = append(notes, fmt.Sprintf("(收到文件 %s,已存 %s;如需读取请告知)", name, path))
			}
		}
		cancel()
	}
	if len(atts) == 0 && len(notes) > 0 {
		return nil, strings.Join(notes, "\n")
	}
	return atts, strings.Join(notes, "\n")
}

func (t *wechatTransport) setLastError(msg string) {
	t.mu.Lock()
	t.lastError = msg
	t.mu.Unlock()
}

// renderQRText 把二维码内容(iLink qrcode_img_content = 待编码的登录 URL)渲染为 ASCII 二维码文本,
// 终端直接可扫(半块渲染,视觉方正;空输出回退纯 URL 行由调用方兜底)。
func renderQRText(content string) string {
	if content == "" {
		return ""
	}
	var buf strings.Builder
	qrterminal.GenerateWithConfig(content, qrterminal.Config{
		Level: qrterminal.M, Writer: &buf, QuietZone: 1, HalfBlocks: true,
	})
	return strings.TrimRight(buf.String(), "\n")
}

// loginHint 登录指引文本:ASCII 二维码 + URL 兜底行(极窄终端/不支持半块时仍可打开链接)。
func loginHint(qr *ilink.QRResponse) string {
	code := renderQRText(qr.QRCodeImg)
	hint := "请用微信扫描下方二维码登录(二维码不清晰或无法扫描,请打开链接):\n" + code + "\n"
	return hint + "链接: " + qr.QRCodeImg + "\n(等待确认,超时 5 分钟;状态查询 /wechat status)"
}

// wechatCmd /wechat 命令:login/status。
func (t *wechatTransport) wechatCmd(ctx context.Context, args []string) (string, error) {
	if len(args) == 0 || args[0] == "status" {
		return t.statusText(), nil
	}
	if args[0] != "login" {
		return "", fmt.Errorf("用法: /wechat login|status")
	}
	t.mu.Lock()
	if t.loginBusy {
		t.mu.Unlock()
		return "登录进行中,请稍候…", nil
	}
	t.mu.Unlock()
	qr, err := ilink.FetchQR(ctx, t.baseURL)
	if err != nil {
		return "", fmt.Errorf("获取二维码失败: %w", err)
	}
	t.mu.Lock()
	t.loginBusy = true
	t.mu.Unlock()
	go func() {
		defer func() {
			t.mu.Lock()
			t.loginBusy = false
			t.mu.Unlock()
		}()
		t.finishLogin(qr)
	}()
	return loginHint(qr), nil
}

// invalidateCreds 清理失效登录态(token/sync 游标)并落盘——
// 避免重启后带着失效凭证反复失败;用户重扫后恢复正常。
func (t *wechatTransport) invalidateCreds() {
	t.mu.Lock()
	if t.creds == nil {
		t.mu.Unlock()
		return
	}
	t.creds.Token = ""
	t.creds.SyncBuf = ""
	t.client = nil
	creds := t.creds
	store := t.store
	t.mu.Unlock()
	if err := store.Save(creds); err != nil {
		t.setLastError("失效凭证清理落盘失败: " + err.Error())
	}
}

// disconnect E3-R:断开连接并清理本地凭证(退登;授权名单保留),幂等。
// 与 invalidateCreds 的区别:先停轮询(会话过期场景轮询已自停),并显式回退连接卡相位。
func (t *wechatTransport) disconnect() error {
	t.stopPoll()
	t.mu.Lock()
	t.client = nil
	if t.creds != nil {
		t.creds.Token = ""
		t.creds.SyncBuf = ""
	}
	creds, store := t.creds, t.store
	t.mu.Unlock()
	if creds != nil && store != nil {
		if err := store.Save(creds); err != nil {
			return err
		}
	}
	t.setLastError("未登录(执行 /wechat login)")
	t.setLoginState(sdk.IMLoginState{Phase: "idle", Detail: "已退出登录"})
	t.setConn(sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseIdle, Detail: "已退出登录"})
	return nil
}

// autoLogin 未登录自动扫码:取二维码 → 终端渲染 ASCII 二维码 + URL(stderr;headless 可见)→ 后台轮询确认。
func (t *wechatTransport) autoLogin() {
	qr, err := ilink.FetchQR(context.Background(), t.baseURL)
	if err != nil {
		t.setLastError("获取登录二维码失败: " + err.Error())
		return
	}
	code := renderQRText(qr.QRCodeImg)
	fmt.Fprintf(os.Stderr, "\n[wechat] 请用微信扫描下方二维码登录(无法扫描请打开链接):\n%s\n链接: %s\n等待手机确认(5 分钟内)…\n", code, qr.QRCodeImg)
	t.finishLogin(qr)
}

// finishLogin 后台完成扫码登录(E1:过期自动重取 + 相位回显):
//   - 相位(等待扫码/已扫码/过期重取/保存)经 stderr 一行行回显(终端直接可见);
//   - 二维码过期时**自动重取并在终端重绘**新码(旧行为:打印一次,过期需用户重按);
//   - 成功后存凭证 + 授权扫码者 + 启动 poll。
//
// 返回错误供面板登录流程记录进度(旧调用方忽略)。
func (t *wechatTransport) finishLogin(qr *ilink.QRResponse) error {
	creds, err := ilink.LoginQRWithProgress(context.Background(), t.baseURL, 10*time.Minute,
		func(nq *ilink.QRResponse) {
			code := renderQRText(nq.QRCodeImg)
			fmt.Fprintf(os.Stderr, "\n[wechat] 新二维码(请用微信扫描;无法扫描请打开链接):\n%s\n链接: %s\n", code, nq.QRCodeImg)
			t.setConn(sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseWaitingScan,
				Detail: "请用微信扫码并在手机上确认", QRContent: nq.QRCodeImg})
		},
		func(phase, detail string) {
			fmt.Fprintf(os.Stderr, "[wechat] %s\n", detail)
			if phase == "validating" {
				t.setConnPhase(sdk.IMPhaseValidating, detail)
				return
			}
			t.setConnPhase(phase, detail)
		})
	if err != nil {
		t.setLastError("登录失败: " + err.Error())
		t.setConn(sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseFailed, Error: err.Error()})
		return err
	}
	if err := t.applyCreds(creds); err != nil {
		t.setConn(sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseFailed, Error: err.Error()})
		return err
	}
	t.setConn(sdk.IMConnectStatus{Channel: t.name, Phase: sdk.IMPhaseDone, Detail: "登录成功",
		Account: maskTail(creds.AccountID)})
	return nil
}

// statusText 状态文本。
func (t *wechatTransport) statusText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := "未登录"
	acct := ""
	if t.creds.Token != "" {
		state = "已登录"
		acct = t.creds.AccountID
		if len(t.creds.Token) > 8 {
			acct += " token…" + t.creds.Token[len(t.creds.Token)-4:]
		}
	}
	polling := "停"
	if t.polling {
		polling = "运行"
	}
	allowed := len(t.creds.Allow)
	if t.bridge != nil {
		allowed = len(t.bridge.Access().List())
	}
	return fmt.Sprintf("wechat: %s(%s) 轮询=%s 已授权=%d\n最近: %s", state, acct, polling, allowed, t.lastError)
}

func hasStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
