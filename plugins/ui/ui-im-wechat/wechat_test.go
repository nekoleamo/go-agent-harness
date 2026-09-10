// ui-im-wechat transport 纯函数单测:长文本分块策略(2000 字/段落优先/截断提示)。
package uimwechat

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/ilink"
	"github.com/nekoleamo/go-agent-harness/im"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestSplitLongText 分块:短文本单块;长文本按段/行/空格/硬切;字符数不超上限。
func TestSplitLongText(t *testing.T) {
	short := "你好"
	if got := im.SplitText(short, wechatChunkLimit); len(got) != 1 || got[0] != short {
		t.Fatalf("短文本应单块: %v", got)
	}
	// 超长无任何边界 → 硬切为多块,每块(除末块)<= limit 且总拼接还原非空
	long := strings.Repeat("字", wechatChunkLimit*2+10)
	chunks := im.SplitText(long, wechatChunkLimit)
	if len(chunks) < 3 {
		t.Fatalf("应切成 >=3 块,got %d", len(chunks))
	}
	for i, c := range chunks {
		if n := len([]rune(c)); n > wechatChunkLimit {
			t.Fatalf("块 %d 超限 %d > %d", i, n, wechatChunkLimit)
		}
		if !utf8.ValidString(c) {
			t.Fatalf("块 %d 非法 UTF-8(切点截断多字节字符)", i)
		}
	}
	if joined := strings.Join(chunks, ""); len([]rune(joined)) != len([]rune(long)) {
		t.Fatalf("拼接长度不符: %d vs %d", len([]rune(joined)), len([]rune(long)))
	}
	// 段落边界优先:首块应在 \n\n 处断开,不硬切长词
	para := strings.Repeat("甲", wechatChunkLimit/2) + "\n\n" + strings.Repeat("乙", 3000)
	chunks = im.SplitText(para, wechatChunkLimit)
	if len(chunks) < 2 {
		t.Fatalf("段落文本应 >=2 块,got %d", len(chunks))
	}
}

// TestSplitLongTextTrimSafe 空格切分不产生空块;TrimSpace 后空文本不 panic。
func TestSplitLongTextTrimSafe(t *testing.T) {
	// 纯空格长文本:TrimSpace 后为空 → 应回退单块原文本(不 panic)
	long := strings.Repeat(" ", 2500)
	chunks := im.SplitText(long, wechatChunkLimit)
	if len(chunks) == 0 || chunks[0] == "" {
		t.Fatalf("空/空白输入应安全返回,got %v", chunks)
	}
}

// TestRenderQRText 终端二维码渲染:URL → ASCII 二维码文本(含半块字符,可扫);空输入安全返回。
func TestRenderQRText(t *testing.T) {
	url := "https://weixin.qq.com/x/cAbCdEfGhIjK"
	s := renderQRText(url)
	if s == "" {
		t.Fatal("二维码渲染为空")
	}
	hasBlock := false
	for _, ch := range []string{"█", "▀", "▄", "▌", "▐"} {
		if strings.Contains(s, ch) {
			hasBlock = true
			break
		}
	}
	if !hasBlock {
		t.Fatalf("二维码应含块状字符(半块渲染):\n%s", s)
	}
	// 空输入安全
	if s2 := renderQRText(""); s2 != "" {
		t.Fatalf("空输入应返回空,got %q", s2)
	}
	// 登录指引含二维码 + URL 兜底行
	hint := loginHint(&ilink.QRResponse{QRCode: "qr-1", QRCodeImg: url})
	if !strings.Contains(hint, url) || !strings.Contains(hint, "█") {
		t.Fatalf("登录指引应含二维码块与 URL 兜底: %s", hint)
	}
}

// TestMediaExtractImage 图片入站 → 下载 → 视觉附件(Path 存在、MimeType 推断)。
func TestMediaExtractImage(t *testing.T) {
	payload := []byte("fake-jpeg-bytes")
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer hs.Close()
	tr := &wechatTransport{name: channelName, lastError: ""}
	tr.mu.Lock()
	tr.creds = &ilink.Credentials{}
	tr.mu.Unlock()
	msg := &ilink.InboundMessage{FromUserID: "u1", ItemList: []ilink.Item{
		{Type: 2, ImageItem: &ilink.ImageItem{URL: hs.URL + "/img.jpg?x=1", AesKey: ""}},
	}}
	atts, note := tr.mediaExtract(msg)
	if len(atts) != 1 || atts[0].Kind != sdk.AttachmentImage {
		t.Fatalf("图片应产出视觉附件: %+v", atts)
	}
	if _, err := os.Stat(atts[0].Path); err != nil {
		t.Fatalf("媒体应落盘: %v", err)
	}
	if !strings.Contains(note, "图片") {
		t.Fatalf("说明应含图片提示: %q", note)
	}
}

// TestMediaExtractTextFile 文本文件 → 内容并入正文;二进制文件 → 落盘+路径说明。
func TestMediaExtractTextFile(t *testing.T) {
	payload := []byte("hello from im file 内容")
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer hs.Close()
	tr := &wechatTransport{name: channelName}
	msg := &ilink.InboundMessage{FromUserID: "u1", ItemList: []ilink.Item{
		{Type: 4, FileItem: &ilink.FileItem{FileName: "note.txt", Media: &ilink.Media{FullURL: hs.URL + "/note.txt"}}},
		{Type: 4, FileItem: &ilink.FileItem{FileName: "a.bin", Media: &ilink.Media{FullURL: hs.URL + "/a.bin"}}},
	}}
	atts, note := tr.mediaExtract(msg)
	if !strings.Contains(note, "hello from im file") || !strings.Contains(note, "note.txt") {
		t.Fatalf("文本文件内容应并入正文: %q", note)
	}
	if !strings.Contains(note, "a.bin") {
		t.Fatalf("二进制文件应落盘说明: %q", note)
	}
	// 二进制经 Attachments(File)引用路径
	hasFile := false
	for _, a := range atts {
		if a.Kind == sdk.AttachmentFile && strings.HasSuffix(a.Path, ".bin") {
			hasFile = true
		}
	}
	if !hasFile {
		t.Fatalf("二进制文件应有落盘附件: %+v", atts)
	}
}

// TestMediaExtractDownFail 下载失败 → 不产出附件/正文,不 panic(诊断记入 lastError)。
func TestMediaExtractDownFail(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer hs.Close()
	tr := &wechatTransport{name: channelName}
	msg := &ilink.InboundMessage{FromUserID: "u1", ItemList: []ilink.Item{
		{Type: 2, ImageItem: &ilink.ImageItem{URL: hs.URL + "/bad.jpg"}},
	}}
	atts, note := tr.mediaExtract(msg)
	if len(atts) != 0 || note != "" {
		t.Fatalf("下载失败应跳过: atts=%+v note=%q", atts, note)
	}
	if tr.lastError == "" {
		t.Fatal("应记录诊断")
	}
}

// TestContinueTextAndRemainder 截断剩余暂存/取走 + continue 触发词识别。
func TestContinueTextAndRemainder(t *testing.T) {
	for _, kw := range []string{"continue", "继续", "Continue ", "更多", "next"} {
		if !isContinueText(kw) {
			t.Fatalf("%q 应识别为续取指令", kw)
		}
	}
	for _, not := range []string{"你好", "continu", "", "再继续"} {
		if isContinueText(not) {
			t.Fatalf("%q 不应是续取指令", not)
		}
	}
	tr := &wechatTransport{name: channelName, remainder: map[string]string{"u1": "剩余内容"}}
	if got := tr.takeRemainder("u1"); got != "剩余内容" {
		t.Fatalf("应取走剩余: %q", got)
	}
	if got := tr.takeRemainder("u1"); got != "" {
		t.Fatalf("取走后应清空: %q", got)
	}
}

// TestChannelStatusImplementsLoginProvider 面板契约:ctx.imChannels 提供的适配器
// 必须实现 sdk.IMLoginProvider(否则 Web 面板扫码入口 503)。
func TestChannelStatusImplementsLoginProvider(t *testing.T) {
	var v any = imChannelStatus{tr: &wechatTransport{name: channelName}}
	if _, ok := v.(sdk.IMLoginProvider); !ok {
		t.Fatal("imChannelStatus 应实现 sdk.IMLoginProvider")
	}
	// 未登录态查询应返回 idle
	st := imChannelStatus{tr: &wechatTransport{name: channelName}}.LoginState()
	if st.Phase != "idle" {
		t.Fatalf("未登录应 idle: %+v", st)
	}
}
