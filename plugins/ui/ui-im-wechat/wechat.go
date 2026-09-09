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
		lastError: "未登录(执行 /wechat login)", tickets: make(map[string]ticketEntry)}
	b := im.New(nil, loop, sessions, tr, im.Options{
		Mode:  mode,
		Allow: creds.Allow, // 已授权用户持久恢复
	})
	tr.bridge = b
	// 授权变化持久化(/im pair 批准、allow/revoke、登录授权):写回凭证 store,
	// 重启恢复——配对批准不再因重启丢失(P1 真机需求)。回调锁外触发,List 安全。
	b.Access().SetOnChange(func() {
		allow := b.Access().List()
		tr.mu.Lock()
		tr.creds.Allow = allow
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
	// ctx.confirm = IM 桥(与 tui/web 互斥由 profile)
	if err := c.Provide("ctx.confirm", b); err != nil {
		return nil, err
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
		})
		if err != nil {
			return nil, err
		}
		ds = append(ds, d2)
	}
	return func() {
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

	mu        sync.Mutex
	polling   bool
	stopCh    chan struct{}
	lastError string
	tickets   map[string]ticketEntry
	tokens    map[string]string // userID → context_token(iLink 回显必须)
	loginBusy bool
	typingCtl context.CancelFunc // 回合进行中的 typing 周期刷新控制器(回合结束取消)
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
	// 长回复分块(iLink 单条兼容上限 ~2000 字;短窗口条数受限 ~10 条,过长提示截断):
	// 分块间小间隔避免连发触发窗口截断(社区头号坑:hermes/cc-connect 长回复尾部静默丢失)。
	chunks := splitLongText(text)
	n := len(chunks)
	if n > wechatMaxChunks {
		chunks = chunks[:wechatMaxChunks]
	}
	for i, ch := range chunks {
		if err := cli.SendMessage(ctx, to.UserID, ch, token, ""); err != nil {
			if n > wechatMaxChunks {
				_ = cli.SendMessage(context.Background(), to.UserID, "⚠️ 回复过长已截断;请回复 continue 获取剩余内容", token, "")
			}
			return err
		}
		if i < len(chunks)-1 {
			time.Sleep(wechatChunkGap)
		}
	}
	if n > wechatMaxChunks {
		return cli.SendMessage(context.Background(), to.UserID, "⚠️ 回复过长已截断;请回复 continue 获取剩余内容", token, "")
	}
	return nil
}

// splitLongText 按 ~2000 字切分:优先段落(空行)→ 行 → 空格 → 硬切。
// 全程在 []rune 空间切(rune 安全:块均合法 UTF-8,不会从多字节字符中间截断产生乱码)。
func splitLongText(text string) []string {
	rs := []rune(text)
	if len(rs) <= wechatChunkLimit {
		return []string{text}
	}
	var chunks []string
	for start := 0; start < len(rs); {
		end := wechatCutRunes(rs, start, wechatChunkLimit)
		if piece := strings.TrimSpace(string(rs[start:end])); piece != "" {
			chunks = append(chunks, piece)
		}
		if end <= start {
			break // 防御:切点不推进则终止
		}
		start = end
	}
	if len(chunks) == 0 {
		chunks = []string{text}
	}
	return chunks
}

// wechatCutRunes 在 rs[start:start+limit] 内找最佳切点(rune 下标):段落空行 > 换行 > 空格 > 硬切。
func wechatCutRunes(rs []rune, start, limit int) int {
	end := start + limit
	if end >= len(rs) {
		return len(rs)
	}
	window := string(rs[start:end])
	for _, sep := range []string{"\n\n", "\n", " "} {
		if i := strings.LastIndex(window, sep); i > 0 {
			return start + len([]rune(window[:i])) + len([]rune(sep))
		}
	}
	return end
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
				t.setLastError("微信会话已过期(ret=-14),请重新执行 /wechat login")
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
	text := msg.ExtractText()
	if text == "" {
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
	_ = t.bridge.HandleInbound(context.Background(), im.Inbound{Route: route, MsgID: msgID, Text: text})
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

// finishLogin 后台完成扫码登录:轮询确认 → 存凭证 → 授权扫码者 → 启动 poll。
func (t *wechatTransport) finishLogin(qr *ilink.QRResponse) {
	creds, err := ilink.LoginQRFromToken(context.Background(), t.baseURL, qr.QRCode, 5*time.Minute)
	if err != nil {
		t.setLastError("登录失败: " + err.Error())
		return
	}
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
		return
	}
	t.client = ilink.New(t.creds.BaseURL, t.creds.Token)
	t.mu.Unlock()
	t.bridge.Access().Allow(channelName + "\x00" + creds.UserID)
	t.setLastError("")
	t.startPoll()
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
