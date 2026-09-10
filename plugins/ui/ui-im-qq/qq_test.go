// ui-im-qq transport 纯函数单测:QQ 文本分块(4000/段落优先/截断;镜像微信线策略,常量不同)、
// /qq login 无参输出 AppID/AppSecret 获取指引(与真机验收"填 AppID/AppSecret"对齐)、凭证掩码。
package uimqq

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/im"
	"github.com/nekoleamo/go-agent-harness/qqbot"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestSplitQQText 短文本单块;长文本按段/行/空格/硬切;每块 <=4000。
func TestSplitQQText(t *testing.T) {
	short := "你好"
	if got := im.SplitText(short, qqChunkLimit); len(got) != 1 || got[0] != short {
		t.Fatalf("短文本应单块: %v", got)
	}
	long := strings.Repeat("字", qqChunkLimit*2+10)
	chunks := im.SplitText(long, qqChunkLimit)
	if len(chunks) < 3 {
		t.Fatalf("应切成 >=3 块,got %d", len(chunks))
	}
	for i, c := range chunks {
		if n := len([]rune(c)); n > qqChunkLimit {
			t.Fatalf("块 %d 超限 %d > %d", i, n, qqChunkLimit)
		}
		if !utf8.ValidString(c) {
			t.Fatalf("块 %d 非法 UTF-8(切点截断多字节字符)", i)
		}
	}
	// 段落边界优先
	para := strings.Repeat("甲", qqChunkLimit/2) + "\n\n" + strings.Repeat("乙", 6000)
	if chunks = im.SplitText(para, qqChunkLimit); len(chunks) < 2 {
		t.Fatalf("段落文本应 >=2 块,got %d", len(chunks))
	}
	// 纯空白不 panic
	if chunks = im.SplitText(strings.Repeat(" ", qqChunkLimit+10), qqChunkLimit); len(chunks) == 0 {
		t.Fatal("空白输入应安全返回")
	}
}

// TestQQCmdLoginGuide 无参 /qq login 输出指引(含获取位置与两种填入方式),不落盘。
func TestQQCmdLoginGuide(t *testing.T) {
	tr := &qqTransport{name: channelName, lastError: "未配置(执行 /qq login)"}
	tr.store = qqbot.NewStore(t.TempDir() + "/qqbot.yaml")
	out, err := tr.qqCmd(nil, []string{"login"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "AppID") || !strings.Contains(out, "AppSecret") ||
		!strings.Contains(out, "q.qq.com") || !strings.Contains(out, "/qq login <AppID>") {
		t.Fatalf("登录指引缺关键内容: %s", out)
	}
	if len(out) > 400 {
		t.Fatalf("指引过长(%d),应简洁", len(out))
	}
	// /qq status 无凭证:状态文本含"未配置"
	st, _ := tr.qqCmd(nil, []string{"status"})
	if !strings.Contains(st, "未配置") || !strings.Contains(st, "/qq login") {
		t.Fatalf("status 不符: %s", st)
	}
}

// TestQQCmdLoginPersist 带参登录落盘凭证(0600)与 AppSecret 掩码展示。
func TestQQCmdLoginPersist(t *testing.T) {
	tr := &qqTransport{name: channelName, lastError: "未配置"}
	tr.store = qqbot.NewStore(t.TempDir() + "/qqbot.yaml")
	out, err := tr.qqCmd(nil, []string{"login", "APP123456", "SECRET-abc-xyz"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已保存") {
		t.Fatalf("登录返回不符: %s", out)
	}
	// 凭证落盘且已配置
	creds, err := tr.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if creds.AppID != "APP123456" || creds.AppSecret != "SECRET-abc-xyz" {
		t.Fatalf("凭证不符: %+v", creds)
	}
	// status 展示掩码(不泄露全量 AppSecret 也不显示 secret)
	st := tr.statusText()
	if !strings.Contains(st, "APP1") || strings.Contains(st, "SECRET-abc-xyz") {
		t.Fatalf("status 掩码不符: %s", st)
	}
}

// TestIsRichText 呈现决策:代码围栏/列表 → markdown;纯短句/普通段落 → 纯文本。
func TestIsRichText(t *testing.T) {
	md := []string{
		"```go\nfunc main() {}\n```",
		"- 步骤一\n- 步骤二",
		"* 要点\n* 补充",
		"1. 第一\n2. 第二",
		"执行结果:\n  1) 成功\n  2) 警告",
	}
	for _, s := range md {
		if !isRichText(s) {
			t.Fatalf("应判定富文本: %q", s)
		}
	}
	plain := []string{
		"任务完成",
		"这是一段普通说明文字,没有任何列表或代码。",
		"-x 不是列表",
		"12% 的进度",
		"邮箱 a@b.com",
	}
	for _, s := range plain {
		if isRichText(s) {
			t.Fatalf("不应判定富文本: %q", s)
		}
	}
}

// TestActiveQuotaAllowConsume 配额 2 条/天:Consume 扣减,超限 Allow=false,Remaining 归零。
func TestActiveQuotaAllowConsume(t *testing.T) {
	q := newActiveQuota("") // 纯内存
	sender := "qq\x00OPENID1"
	for i := 0; i < q.maxPerDay; i++ {
		if !q.Allow(sender) {
			t.Fatalf("第 %d 次应有预算", i+1)
		}
		if err := q.Consume(sender); err != nil {
			t.Fatal(err)
		}
	}
	if q.Allow(sender) {
		t.Fatal("超限后不应再有预算")
	}
	if r := q.Remaining(sender); r != 0 {
		t.Fatalf("剩余应为 0,got %d", r)
	}
	if r := q.Remaining("qq\x00other"); r != q.maxPerDay {
		t.Fatalf("未用用户应满额,got %d", r)
	}
}

// TestActiveQuotaPersist 落盘往返:同日重启继承已用额度(重启不超发),文件 0600。
func TestActiveQuotaPersist(t *testing.T) {
	path := t.TempDir() + "/qqbot-quota.yaml"
	q := newActiveQuota(path)
	sender := "qq\x00OPENID1"
	if err := q.Consume(sender); err != nil {
		t.Fatal(err)
	}
	// 重建(模拟重启):同日应继承已用 1 条 → 剩 1
	q2 := newActiveQuota(path)
	if q2.Allow(sender) != true || q2.Remaining(sender) != q.maxPerDay-1 {
		t.Fatalf("重启应继承额度,remaining=%d", q2.Remaining(sender))
	}
	if err := q2.Consume(sender); err != nil {
		t.Fatal(err)
	}
	if q2.Allow(sender) {
		t.Fatal("继承 + 再扣一条应超限")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("配额文件应 0600,got %v", fi.Mode().Perm())
	}
}

// TestActiveQuotaCrossDayReset 跨日文件(旧 day)不继承(新一天预算重置)。
func TestActiveQuotaCrossDayReset(t *testing.T) {
	path := t.TempDir() + "/qqbot-quota.yaml"
	os.WriteFile(path, []byte("day: 2020-01-01\nused:\n  \"qq\\x00OPENID1\": 2\n"), 0o600)
	q := newActiveQuota(path)
	if !q.Allow("qq\x00OPENID1") {
		t.Fatal("跨日应重置预算(旧日已用不继承)")
	}
}

// TestActiveOneMessage 主动合并单条:短文原样;超 4000 截断 ≤4000 且带截断提示。
func TestActiveOneMessage(t *testing.T) {
	short := "任务完成"
	msg := activeOneMessage(short)
	if msg.MsgType != 0 || msg.Content != short {
		t.Fatalf("短文主动原样: %+v", msg)
	}
	long := strings.Repeat("很长的输出", 3000) // 21000 字
	msg = activeOneMessage(long)
	if n := len([]rune(msg.Content)); n > 4000 {
		t.Fatalf("主动截断应 ≤4000,got %d", n)
	}
	if !strings.Contains(msg.Content, "截断") {
		t.Fatalf("截断应带提示: %q", msg.Content[len(msg.Content)-30:])
	}
}

// TestQQLoginVerifyFail login 后即时校验 token:失败应返回明确原因(不再静默)。
func TestQQLoginVerifyFail(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":100007,"message":"invalid appid or secret"}`, http.StatusUnauthorized)
	}))
	defer hs.Close()
	tr := &qqTransport{name: channelName, store: qqbot.NewStore(t.TempDir() + "/qqbot.yaml"),
		baseURL: qqbot.DefaultBaseURL, tokenURL: hs.URL, lastError: ""}
	err := tr.login("app-x", "sec-y")
	if err == nil || !strings.Contains(err.Error(), "校验 access_token 失败") {
		t.Fatalf("凭证无效应返回明确校验错误: %v", err)
	}
	if !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("错误应含排查指引(沙箱等): %v", err)
	}
	// 凭证仍已保存(用户可改 env 后重启)
	if c, lerr := tr.store.Load(); lerr != nil || c.AppID != "app-x" {
		t.Fatalf("凭证应已保存: %+v %v", c, lerr)
	}
}

// TestQQLoginVerifyOK 校验通过:无错误返回。
func TestQQLoginVerifyOK(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"tk-1","expires_in":7200}`))
	}))
	defer hs.Close()
	tr := &qqTransport{name: channelName, store: qqbot.NewStore(t.TempDir() + "/qqbot.yaml"),
		baseURL: qqbot.DefaultBaseURL, tokenURL: hs.URL}
	if err := tr.login("app-x", "sec-y"); err != nil {
		t.Fatalf("校验通过不应报错: %v", err)
	}
	tr.stopGateway()
}

// TestQQEnvSwitch /qq env:查询当前 / 切沙箱(持久到凭证) / 切正式。
func TestQQEnvSwitch(t *testing.T) {
	tr := &qqTransport{name: channelName, store: qqbot.NewStore(t.TempDir() + "/qqbot.yaml"),
		baseURL: qqbot.DefaultBaseURL, tokenURL: "http://127.0.0.1:1/unused"}
	out, err := tr.envCmd([]string{"env"})
	if err != nil || !strings.Contains(out, qqbot.DefaultBaseURL) {
		t.Fatalf("应显示当前环境: %q %v", out, err)
	}
	if _, err := tr.envCmd([]string{"env", "sandbox"}); err != nil {
		t.Fatal(err)
	}
	c, _ := tr.store.Load()
	if c.BaseURL != "https://sandbox.api.sgroup.qq.com" {
		t.Fatalf("沙箱应持久到凭证: %+v", c)
	}
	// startGateway 优先用凭证 BaseURL
	tr.mu.Lock()
	got := tr.creds.BaseURL
	tr.mu.Unlock()
	if got != "https://sandbox.api.sgroup.qq.com" {
		t.Fatalf("transport 凭证应同步: %q", got)
	}
	if _, err := tr.envCmd([]string{"env", "official"}); err != nil {
		t.Fatal(err)
	}
	if c2, _ := tr.store.Load(); c2.BaseURL != qqbot.DefaultBaseURL {
		t.Fatalf("切回正式不符: %+v", c2)
	}
}

// TestQQStatusShowsBaseURL 状态文本含环境标识(便于诊断沙箱/正式)。
func TestQQStatusShowsBaseURL(t *testing.T) {
	tr := &qqTransport{name: channelName, store: qqbot.NewStore(t.TempDir() + "/qqbot.yaml"),
		baseURL: qqbot.DefaultBaseURL,
		creds:   &qqbot.Credentials{AppID: "1234567890", AppSecret: "s", BaseURL: "https://sandbox.api.sgroup.qq.com"}}
	out := tr.statusText()
	if !strings.Contains(out, "sandbox.api.sgroup.qq.com") {
		t.Fatalf("状态应显示当前环境: %q", out)
	}
	if !strings.Contains(out, "最近:") {
		t.Fatalf("状态应含诊断行: %q", out)
	}
}

// TestFlushRemainder continue 续发:有剩余 → 消费并清空(发送失败也清,防重复轰炸);
// 无剩余/非续取词 → false(交给桥当普通消息)。
func TestFlushRemainder(t *testing.T) {
	tr := &qqTransport{name: channelName, remainder: map[string]string{"C1": "剩余文本"}}
	route := im.Route{Channel: channelName, UserID: "U1", ChatID: "C1"}
	if tr.flushRemainder(route, "你好") {
		t.Fatal("非续取词不应消费")
	}
	if !tr.flushRemainder(route, "continue") {
		t.Fatal("continue 且有剩余应消费")
	}
	if tr.remainder["C1"] != "" {
		t.Fatalf("消费后应清空: %q", tr.remainder["C1"])
	}
	if tr.flushRemainder(route, "继续") {
		t.Fatal("无剩余不应消费(交桥处理)")
	}
}

// TestQQMediaExtract 入站附件:图片→视觉附件落盘;文本类→内容并入正文;语音→说明;失败→诊断不阻断。
func TestQQMediaExtract(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/p.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("fake-jpeg"))
		case "/a.txt":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("文件内容 hello"))
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer hs.Close()
	tr := &qqTransport{name: channelName, client: qqbot.NewClient(nil)}

	// 图片 → 视觉附件(路径存在)+ 说明
	atts, note := tr.mediaExtract([]qqbot.MessageAttachment{{URL: hs.URL + "/p.jpg", ContentType: "image/jpeg"}}, "U1")
	if len(atts) != 1 || atts[0].Kind != sdk.AttachmentImage || atts[0].MimeType != "image/jpeg" {
		t.Fatalf("图片应产视觉附件: %+v", atts)
	}
	if _, err := os.Stat(atts[0].Path); err != nil {
		t.Fatalf("应落盘: %v", err)
	}
	if !strings.Contains(note, "图片") {
		t.Fatalf("说明应含图片提示: %q", note)
	}
	// 文本类文件 → 内容并入正文
	atts2, note2 := tr.mediaExtract([]qqbot.MessageAttachment{{URL: hs.URL + "/a.txt", ContentType: "text/plain", FileName: "a.txt"}}, "U1")
	if len(atts2) != 0 || !strings.Contains(note2, "文件内容 hello") || !strings.Contains(note2, "a.txt") {
		t.Fatalf("文本类应并入正文: %+v %q", atts2, note2)
	}
	// 语音 → 说明(不落盘)
	_, note3 := tr.mediaExtract([]qqbot.MessageAttachment{{URL: hs.URL + "/v.mp3", ContentType: "voice"}}, "U1")
	if !strings.Contains(note3, "语音") {
		t.Fatalf("语音应给说明: %q", note3)
	}
	// 下载失败 → 说明 + 诊断,不 panic
	_, note4 := tr.mediaExtract([]qqbot.MessageAttachment{{URL: hs.URL + "/bad", ContentType: "image/png"}}, "U1")
	if !strings.Contains(note4, "下载失败") || tr.lastError == "" {
		t.Fatalf("失败应给说明与诊断: %q err=%q", note4, tr.lastError)
	}
	// 无 URL → 说明
	_, note5 := tr.mediaExtract([]qqbot.MessageAttachment{{ContentType: "image/png"}}, "U1")
	if !strings.Contains(note5, "未给下载地址") {
		t.Fatalf("缺地址应给说明: %q", note5)
	}
}

// TestMentionsBotOfficialSemantics 群 @ 判定回归护栏(真机 bug:群内 @ 机器人无反应)。
// 官方语义:GROUP_AT_MESSAGE_CREATE 仅在 @ 机器人时推送,且 mentions **不含机器人自身**
// (bot.q.qq.com 群@机器人消息)→ 不能以"mentions 含机器人"为触发前提;真机 mentions 常为空。
func TestMentionsBotOfficialSemantics(t *testing.T) {
	cases := []struct {
		name     string
		bot      string
		mentions []qqbot.Mention
		want     bool
	}{
		{"mentions 空(真机常见形态)", "BOTOPENID", nil, true},
		{"mentions 仅含其它被 @ 用户(不含机器人)", "BOTOPENID", []qqbot.Mention{{ID: "U1", Username: "小明"}}, true},
		{"mentions 显式含机器人自身 id", "BOTOPENID", []qqbot.Mention{{ID: "BOTOPENID", Bot: true}}, true},
		{"mentions 全为其它机器人", "BOTOPENID", []qqbot.Mention{{ID: "OTHERBOT", Bot: true}}, false},
		{"未 Ready(botOpenID 空)+ 其它机器人", "", []qqbot.Mention{{ID: "OTHERBOT", Bot: true}}, false},
		{"未 Ready(botOpenID 空)+ mentions 空", "", nil, true},
	}
	for _, c := range cases {
		tr := &qqTransport{botOpenID: c.bot}
		if got := tr.mentionsBot(&qqbot.GroupAtMessage{Mentions: c.mentions}); got != c.want {
			t.Fatalf("%s: mentionsBot=%v want %v", c.name, got, c.want)
		}
	}
}

// TestIsSelfMessage 机器人自身群消息判定(防自问自答回环)。
func TestIsSelfMessage(t *testing.T) {
	tr := &qqTransport{botOpenID: "BOTOPENID"}
	if !tr.isSelfMessage(&qqbot.GroupAtMessage{Author: qqbot.Author{MemberOpenID: "BOTOPENID", Bot: true}}) {
		t.Fatal("机器人自身 member_openid 应判为自身消息")
	}
	if tr.isSelfMessage(&qqbot.GroupAtMessage{Author: qqbot.Author{MemberOpenID: "MEMBER9", Bot: false}}) {
		t.Fatal("普通成员不应判为自身消息")
	}
	empty := &qqTransport{}
	if empty.isSelfMessage(&qqbot.GroupAtMessage{Author: qqbot.Author{MemberOpenID: "X"}}) {
		t.Fatal("未 Ready 时不应误判")
	}
}

// TestDiagRing diagf 诊断环形缓冲(真机排障可见性):累积 + 上限截断 + 最近优先。
func TestDiagRing(t *testing.T) {
	tr := &qqTransport{}
	for i := 0; i < 45; i++ {
		tr.diagf("事件 %d", i)
	}
	lines := strings.Split(tr.diagLines(100), "\n")
	if len(lines) != 40 {
		t.Fatalf("环形上限应为 40 条,got %d", len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], "事件 44") {
		t.Fatalf("应保留最新一条: %q", lines[len(lines)-1])
	}
	if !strings.Contains(lines[0], "事件 5") {
		t.Fatalf("最旧应为事件 5: %q", lines[0])
	}
	if got := tr.diagLines(3); len(strings.Split(got, "\n")) != 3 {
		t.Fatalf("diagLines(3) 应取最近 3 条: %q", got)
	}
	if (&qqTransport{}).diagLines(5) != "" {
		t.Fatal("无诊断应返回空串")
	}
}

// TestEnvNameAndGuide envName 环境识别 + /qq env 无参指引含沙箱群聊前置(个人开发者群聊唯一路径)。
func TestEnvNameAndGuide(t *testing.T) {
	if got := envName("https://sandbox.api.sgroup.qq.com"); !strings.Contains(got, "沙箱") {
		t.Fatalf("沙箱 URL 应识别为沙箱: %q", got)
	}
	if got := envName(qqbot.DefaultBaseURL); !strings.Contains(got, "正式") {
		t.Fatalf("官方根应识别为正式: %q", got)
	}
	if got := envName("https://example.com"); got != "自定义" {
		t.Fatalf("未知 URL 应为自定义: %q", got)
	}
	tr := &qqTransport{creds: &qqbot.Credentials{AppID: "a", AppSecret: "b"}, baseURL: qqbot.DefaultBaseURL}
	out, err := tr.qqCmd(context.Background(), []string{"env"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"沙箱配置页", "qqbot/#/developer/sandbox", "群机器人", "不支持私聊"} {
		if !strings.Contains(out, want) {
			t.Fatalf("env 指引应含 %q:\n%s", want, out)
		}
	}
}

// TestGatewayStoppedGeneration 网关世代语义(修 status 误报"网关=停"):
// 旧世代 goroutine 收尾不得改动新网关状态;同世代才清 running 并记错误。
func TestGatewayStoppedGeneration(t *testing.T) {
	tr := &qqTransport{running: true, gen: 2}
	// 旧世代(gen=1)收尾:不生效
	tr.gatewayStopped(1, nil)
	if !tr.running {
		t.Fatal("旧世代收尾不得清 running(否则切环境后 status 误报网关=停)")
	}
	// 同世代 + 取消:清 running,不写错误
	tr.gatewayStopped(2, context.Canceled)
	if tr.running || tr.lastError != "" {
		t.Fatalf("正常停止应清 running 且无错误: %+v", tr.lastError)
	}
	// 同世代 + 真实错误:记录错误并进诊断
	tr2 := &qqTransport{running: true, gen: 5}
	tr2.gatewayStopped(5, errors.New("ws 拨号失败"))
	if tr2.running || !strings.Contains(tr2.lastError, "ws 拨号失败") {
		t.Fatalf("错误应记入 lastError: %q", tr2.lastError)
	}
	if !strings.Contains(tr2.diagLines(5), "网关停止") {
		t.Fatalf("错误应进诊断环形: %q", tr2.diagLines(5))
	}
	// 旧世代错误也不得覆盖新状态
	tr3 := &qqTransport{running: true, gen: 9}
	tr3.gatewayStopped(8, errors.New("旧错误"))
	if !tr3.running || tr3.lastError != "" {
		t.Fatalf("旧世代错误不得覆盖: running=%v err=%q", tr3.running, tr3.lastError)
	}
}

// TestEventCountsDiag 事件类型计数诊断:分类累计 + /qq status 展示
// (群@=0 长期不变 = 平台侧未推送,用于把"无反应"定位到沙箱配置/认证而非本地代码)。
func TestEventCountsDiag(t *testing.T) {
	tr := &qqTransport{evCounts: map[string]int{}}
	tr.countEvent("group")
	tr.countEvent("group")
	tr.countEvent("c2c")
	tr.countEvent("ready")
	if tr.evCount("group") != 2 || tr.evCount("c2c") != 1 {
		t.Fatalf("计数不符: group=%d c2c=%d", tr.evCount("group"), tr.evCount("c2c"))
	}
	out := tr.statusText()
	if !strings.Contains(out, "事件统计: C2C=1 群@=2 其它=0") {
		t.Fatalf("/qq status 应展示事件统计:\n%s", out)
	}
	// 空 transports(nil map)不 panic
	(&qqTransport{}).statusText()
}

// TestQQDisconnect E3-R:/im logout —— 停网关 + 清凭证(授权/群名单保留)+ 连接卡回 idle。
func TestQQDisconnect(t *testing.T) {
	var v any = imChannelStatus{tr: &qqTransport{name: channelName}}
	if _, ok := v.(sdk.IMDisconnectProvider); !ok {
		t.Fatal("imChannelStatus 应实现 sdk.IMDisconnectProvider")
	}
	dir := t.TempDir()
	store := qqbot.NewStore(dir + "/qqbot.yaml")
	creds := &qqbot.Credentials{AppID: "app-1", AppSecret: "sec-1", BaseURL: "https://sandbox.api.sgroup.qq.com",
		Allow: []string{"qq\x00u1"}, Groups: []string{"qq\x00g1"}}
	if err := store.Save(creds); err != nil {
		t.Fatal(err)
	}
	tr := &qqTransport{name: channelName, store: store, creds: creds, baseURL: qqbot.DefaultBaseURL,
		replies: map[string]*replyCtx{}, seq: map[string]uint64{}, evCounts: map[string]int{},
		conn: sdk.IMConnectStatus{Phase: sdk.IMPhaseDone, Detail: "网关在线"}}
	if err := tr.disconnect(); err != nil {
		t.Fatal(err)
	}
	if tr.creds.Configured() {
		t.Fatalf("应清理凭证: %+v", tr.creds)
	}
	if len(tr.creds.Allow) != 1 || len(tr.creds.Groups) != 1 {
		t.Fatalf("授权名单必须保留: %+v", tr.creds)
	}
	if tr.creds.BaseURL == "" {
		t.Fatal("环境选择应保留(仅清凭证)")
	}
	if got, _ := store.Load(); got.Configured() || len(got.Allow) != 1 {
		t.Fatalf("落盘异常: %+v", got)
	}
	if st := (imChannelStatus{tr: tr}).ConnectStatus(); st.Phase != sdk.IMPhaseIdle {
		t.Fatalf("连接卡应回 idle: %+v", st)
	}
	if tr.running {
		t.Fatal("网关应已停止")
	}
	if !strings.Contains(tr.statusText(), "未配置") {
		t.Fatalf("状态文本应显示未配置: %q", tr.statusText())
	}
	// 幂等
	if err := tr.disconnect(); err != nil {
		t.Fatalf("重复退出应幂等: %v", err)
	}
}
