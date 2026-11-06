package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
)

// TestVersionNewer 版本比较:逐段数字,"v" 前缀与后缀都不影响。
// 反向验证:把比较方向改反,第一条断言必红。
func TestVersionNewer(t *testing.T) {
	cases := []struct {
		remote, current string
		want            bool
	}{
		{"0.1.7", "0.1.6", true},
		{"v0.1.7", "0.1.6", true},
		{"0.1.6", "0.1.6", false},
		{"0.1.5", "0.1.6", false},
		{"0.2", "0.1.9", true},
		{"0.1.7-3-gabc", "0.1.6", true},
		{"0.1.7", "dev", true}, // 开发版本视为最旧
		{"1.0.0", "0.9.9", true},
		// 预发布口径:核心号相同时只有「当前是预发布、远端是正式版」才算更新
		// (否则预发布用户会永久停在预发布版,界面只说「已是最新」)。
		{"0.8.0", "0.8.0-preview", true},
		{"0.8.0-preview", "0.7.4", true},
		{"0.8.0-preview", "0.8.0", false},
		{"0.8.0-preview", "0.8.0-preview", false},
		{"0.8.0+build7", "0.8.0", false}, // + 是 build 元数据,不算预发布
	}
	for _, c := range cases {
		if got := versionNewer(c.remote, c.current); got != c.want {
			t.Errorf("versionNewer(%q,%q)=%v want %v", c.remote, c.current, got, c.want)
		}
	}
}

// TestPlatformKey 清单键沿用旧壳命名(arm64 → aarch64,amd64 → x86_64)。
func TestPlatformKey(t *testing.T) {
	k := platformKey()
	if strings.Contains(k, "arm64") || strings.Contains(k, "amd64") {
		t.Fatalf("清单键不该用 Go 的架构拼法: %q", k)
	}
	if !strings.HasPrefix(k, "darwin-") && !strings.HasPrefix(k, "windows-") && !strings.HasPrefix(k, "linux-") {
		t.Fatalf("清单键形状不对: %q", k)
	}
}

// manifestServer 造一个签名好的清单服务:返回 server 与清单 JSON。
func manifestServer(t *testing.T, version string, artifact []byte, key ed25519.PrivateKey) (*httptest.Server, []byte) {
	t.Helper()
	sum := sha256.Sum256(artifact)
	sig := ed25519.Sign(key, sum[:])
	m := map[string]any{
		"version":  version,
		"notes":    "测试版本",
		"pub_date": "2026-10-29T00:00:00Z",
		"artifacts": map[string]any{
			"testos-testarch": map[string]any{
				"url":    "ARTIFACT_URL",
				"size":   len(artifact),
				"sha256": hex.EncodeToString(sum[:]),
				"sig":    base64.StdEncoding.EncodeToString(sig),
			},
		},
	}
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/artifact.zip") {
			_, _ = w.Write(artifact)
			return
		}
		if body == nil {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	// 第二轮才把 URL 填成真实地址(server 起来才知道端口)。
	m["artifacts"].(map[string]any)["testos-testarch"].(map[string]any)["url"] = srv.URL + "/artifact.zip"
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return srv, body
}

// TestProviderCheckAndDownload 端到端:清单 → 挑平台 → 版本比较 → 摘要与签名 →
// 下载字节。这条用例覆盖了「壳能不能正确认出并装上新版本」的全部判据。
// 反向验证:把签名对象从摘要改成整包(或反过来),摘要/签名断言必红。
func TestProviderCheckAndDownload(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("fake app zip bytes")
	srv, body := manifestServer(t, "0.1.7", artifact, priv)
	defer srv.Close()

	ms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer ms.Close()
	t.Setenv("GAH_DESKTOP_UPDATE_SOURCES", ms.URL+"/manifest.json")

	p := newDesktopProvider()
	req := updater.CheckRequest{CurrentVersion: "0.1.6", Platform: "testos", Arch: "testarch"}

	rel, err := p.Check(context.Background(), req)
	if err != nil || rel == nil {
		t.Fatalf("应发现新版本: rel=%v err=%v", rel, err)
	}
	if rel.Version != "0.1.7" {
		t.Fatalf("版本 = %q", rel.Version)
	}
	if rel.Verification == nil || rel.Verification.SignatureAlgo != "ed25519" {
		t.Fatalf("验签信息缺失: %+v", rel.Verification)
	}
	want := sha256.Sum256(artifact)
	if !bytes.Equal(rel.Verification.Digest, want[:]) {
		t.Fatal("摘要与产物不一致")
	}
	if !ed25519.Verify(pub, want[:], rel.Verification.Signature) {
		t.Fatal("签名与产物不一致(发布工具与壳的签名口径必须都是「对 sha256 摘要签」)")
	}

	// 已是最新 ⇒ (nil, nil),由框架判定「无需更新」。
	req.CurrentVersion = "0.1.7"
	rel2, err := p.Check(context.Background(), req)
	if err != nil || rel2 != nil {
		t.Fatalf("已最新应返回 (nil,nil): rel=%v err=%v", rel2, err)
	}

	// 下载:字节必须与清单指向的产物完全一致。
	var buf bytes.Buffer
	if err := p.Download(context.Background(), rel, &buf, func(int64, int64) {}); err != nil {
		t.Fatalf("Download 失败: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), artifact) {
		t.Fatalf("下载内容不一致: %q", buf.String())
	}
}

// TestDownloadNotLimitedByManifestTimeout 下载不受清单客户端的短超时影响。
//
// 为什么要有这条(2026-10-30 发现):清单与下载原先共用一个 `http.Client{Timeout: 30s}`,
// 而 http.Client.Timeout 是**整个请求(含响应体)**的上限 —— 30–45MB 的升级包在慢网/国内
// 镜像源下常常要一两分钟,于是每一次升级都会在 30 秒处被自己杀掉,界面只说「安装失败」。
// 反向验证:Download 改回用 p.client,本条必红(服务端每字节 40ms、共 360ms)。
func TestDownloadNotLimitedByManifestTimeout(t *testing.T) {
	payload := []byte("hello gah")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.WriteHeader(http.StatusOK)
		for _, b := range payload {
			_, _ = w.Write([]byte{b})
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(40 * time.Millisecond)
		}
	}))
	defer srv.Close()

	p := newDesktopProvider()
	p.client.Timeout = 50 * time.Millisecond // 清单侧刻意设得比整次下载短
	rel := &updater.Release{Metadata: map[string]any{"url": srv.URL + "/a.zip", "source": "test"}}
	var buf bytes.Buffer
	if err := p.Download(context.Background(), rel, &buf, func(int64, int64) {}); err != nil {
		t.Fatalf("下载被清单客户端的超时打断: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), payload) {
		t.Fatalf("下载内容不一致: %q", buf.String())
	}
}

// signedManifest 造一份签名清单(artifact 的 sha256 签名,url 可指向任意地址)。
func signedManifest(t *testing.T, version string, artifact []byte, url string, key ed25519.PrivateKey) []byte {
	t.Helper()
	sum := sha256.Sum256(artifact)
	sig := ed25519.Sign(key, sum[:])
	body, err := json.Marshal(map[string]any{
		"version":  version,
		"pub_date": "2026-10-29T00:00:00Z",
		"artifacts": map[string]any{
			"testos-testarch": map[string]any{
				"url":    url,
				"size":   len(artifact),
				"sha256": hex.EncodeToString(sum[:]),
				"sig":    base64.StdEncoding.EncodeToString(sig),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// manifestBytes 把一段清单 JSON 当成清单端点。
func manifestBytes(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestProviderCheckDigestOnlyWithoutPubKey 壳没内置公钥(开发构建)时,清单只能出摘要。
//
// 为什么要有这条:原先 provider 无条件把清单里的 sig 填进 Verification,而 wails
// 的口径是「有签名但没配公钥 ⇒ 直接拒」(pkg/updater/verify.go runVerification),
// 于是开发构建**永远**装不上更新,报错是用户看不懂的
// "signature requires a public key but none configured"(2026-11-02 真机反馈)。
// 反向验证:把 p.digestOnly 改回 false(即不修),本条的签名断言必红。
func TestProviderCheckDigestOnlyWithoutPubKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("dev build artifact")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(artifact)
	}))
	defer srv.Close()
	ms := manifestBytes(t, signedManifest(t, "0.1.7", artifact, srv.URL+"/a.zip", priv))
	t.Setenv("GAH_DESKTOP_UPDATE_SOURCES", ms.URL+"/m.json")

	p := newDesktopProvider()
	p.digestOnly = true
	rel, err := p.Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.1.6", Platform: "testos", Arch: "testarch"})
	if err != nil || rel == nil {
		t.Fatalf("应发现新版本: rel=%v err=%v", rel, err)
	}
	v := rel.Verification
	if v == nil {
		t.Fatal("无公钥时仍要校验摘要,Verification 不能为空")
	}
	if len(v.Signature) != 0 || v.SignatureAlgo != "" {
		t.Fatalf("无公钥时不能带签名(wails 会直接拒掉): algo=%q siglen=%d", v.SignatureAlgo, len(v.Signature))
	}
	want := sha256.Sum256(artifact)
	if !bytes.Equal(v.Digest, want[:]) {
		t.Fatal("摘要必须仍然校验")
	}

	// 有公钥时(发行构建)签名照旧强制带上。
	p2 := newDesktopProvider()
	rel2, err := p2.Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.1.6", Platform: "testos", Arch: "testarch"})
	if err != nil || rel2 == nil {
		t.Fatalf("应发现新版本: rel=%v err=%v", rel2, err)
	}
	if rel2.Verification.SignatureAlgo != "ed25519" || len(rel2.Verification.Signature) == 0 {
		t.Fatalf("发行构建(配了公钥)必须带签名: %+v", rel2.Verification)
	}
}

// TestDownloadRetriesOtherSourceWhenPrimaryPurged 主源的包已被镜像清理(404)时,
// 从另一个清单里拿**同一个包**(同版本 + 同 sha256)再下一次。
//
// 为什么要有这条:Gitee 只保留最近两个 release 的附件(实测 v0.8.0-preview 的
// .app.zip 已 404,而清单还指着它),而原来 Download 只试清单里那一个 URL ⇒ 用户
// 当下这次升级就走不通,失败记忆要等到**下一次**检查才生效。
// 反向验证:把 Download 里的 altURLs 循环去掉,本条必红(错误变成 HTTP 404)。
func TestDownloadRetriesOtherSourceWhenPrimaryPurged(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("fresh update bytes")
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/alive.zip") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(artifact)
	}))
	defer live.Close()

	// 第一顺位清单指向已被清理的地址;第二顺位清单指向同一版的活地址。
	primary := manifestBytes(t, signedManifest(t, "0.1.7", artifact, live.URL+"/gone.zip", priv))
	alt := manifestBytes(t, signedManifest(t, "0.1.7", artifact, live.URL+"/alive.zip", priv))
	t.Setenv("GAH_DESKTOP_UPDATE_SOURCES", primary.URL+"/m.json,"+alt.URL+"/m.json")

	p := newDesktopProvider()
	rel, err := p.Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.1.6", Platform: "testos", Arch: "testarch"})
	if err != nil || rel == nil {
		t.Fatalf("应发现新版本: rel=%v err=%v", rel, err)
	}
	if u, _ := rel.Metadata["url"].(string); !strings.HasSuffix(u, "/gone.zip") {
		t.Fatalf("本用例要的是「主源已清理」的场景,实际取到 %q", u)
	}
	var buf bytes.Buffer
	if err := p.Download(context.Background(), rel, &buf, func(int64, int64) {}); err != nil {
		t.Fatalf("应换源成功: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), artifact) {
		t.Fatalf("下载内容不一致: %q", buf.String())
	}
}

// TestDownloadDoesNotSwitchSourceAfterPartialBytes 开下就不能换源。
//
// wails 的摘要是**边下载边算**的,中途换源会把两份字节拼进同一个文件 ⇒ 摘要必然
// 不符,错会跑到下游、更难解释。所以只有「一个字节都没落盘」时才能换源。
// 反向验证:把 Download 里的 `if written > 0 { return err }` 去掉,本条必红
// (错误消失、buf 变成 4 字节前缀 + 完整包)。
func TestDownloadDoesNotSwitchSourceAfterPartialBytes(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("0123456789abcdef")
	// 主源先吐 4 字节再掐断连接。
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(artifact)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(artifact[:4])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic(http.ErrAbortHandler)
	}))
	defer broken.Close()
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(artifact)
	}))
	defer live.Close()

	primary := manifestBytes(t, signedManifest(t, "0.1.7", artifact, broken.URL+"/half.zip", priv))
	alt := manifestBytes(t, signedManifest(t, "0.1.7", artifact, live.URL+"/alive.zip", priv))
	t.Setenv("GAH_DESKTOP_UPDATE_SOURCES", primary.URL+"/m.json,"+alt.URL+"/m.json")

	p := newDesktopProvider()
	rel, err := p.Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.1.6", Platform: "testos", Arch: "testarch"})
	if err != nil || rel == nil {
		t.Fatalf("应发现新版本: rel=%v err=%v", rel, err)
	}
	var buf bytes.Buffer
	if err := p.Download(context.Background(), rel, &buf, func(int64, int64) {}); err == nil {
		t.Fatal("已开始落盘后断流必须报错,不能换源把两份字节拼起来")
	}
	if bytes.Equal(buf.Bytes(), artifact) {
		t.Fatal("换源拼出来的文件居然等于完整包 —— 说明换源逻辑吞掉了断流")
	}
	if buf.Len() != 4 {
		t.Fatalf("中途不该再写入任何字节: 实际 %d 字节 %q", buf.Len(), buf.String())
	}
}

// TestProviderFallsBackToSecondSource 第一个源不通 → 自动落到第二个(与旧壳同语义)。
func TestProviderFallsBackToSecondSource(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srv, body := manifestServer(t, "0.1.7", []byte("x"), priv)
	defer srv.Close()
	ms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer ms.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	deadURL := dead.URL
	dead.Close() // 直接关掉:连不上

	t.Setenv("GAH_DESKTOP_UPDATE_SOURCES", deadURL+"/m.json,"+ms.URL+"/m.json")
	p := newDesktopProvider()
	rel, err := p.Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.1.6", Platform: "testos", Arch: "testarch"})
	if err != nil || rel == nil {
		t.Fatalf("应回退到第二个源: rel=%v err=%v", rel, err)
	}
	if got, _ := rel.Metadata["source"].(string); !strings.Contains(got, ms.URL) {
		t.Fatalf("应记下来源为第二个源,得到 %q", got)
	}
}

// TestProviderRejectsBadManifest 清单缺字段/平台不存在时明确报错(不静默当成「已最新」)。
func TestProviderRejectsBadManifest(t *testing.T) {
	cases := []string{
		`{}`,
		`{"version":"0.1.7","artifacts":{}}`,
		`{"version":"0.1.7","artifacts":{"other-os":{"url":"u","sha256":"zz","sig":"AA=="}}}`,
		`not json`,
	}
	for _, body := range cases {
		ms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, body)
		}))
		t.Setenv("GAH_DESKTOP_UPDATE_SOURCES", ms.URL)
		p := newDesktopProvider()
		if _, err := p.Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.1.6", Platform: "testos", Arch: "testarch"}); err == nil {
			t.Fatalf("坏清单应报错: %s", body)
		}
		ms.Close()
	}
}

// TestExplainUpdateError 错误翻人话:归类对、原文不丢(用户反馈时要靠原文定位)。
// 反向验证:把 signature 分支去掉,验签那条断言必红。
func TestExplainUpdateError(t *testing.T) {
	cases := []struct{ in, wantIn string }{
		{"updater: gah-desktop: HTTP 404", "没发布完"},
		{"updater: ed25519 signature did not verify", "验签失败"},
		// 「本机没公钥」与「出版方签名不对」是两件事:前者怪不到发布包头上。
		{"updater: signature requires a public key but none configured", "开发构建"},
		{"updater: a public key is configured, so the release must carry a signature", "开发构建"},
		// 下载 404 ≠ 清单 404:前者是镜像清了附件。
		{"updater: gah-desktop: 下载返回 HTTP 404", "镜像源上已被清理"},
		{"updater: digest mismatch", "摘要不一致"},
		{"dial tcp: connection refused", "网络不可达"},
		{"context deadline exceeded", "超时"},
		{"something weird", "something weird"},
	}
	for _, c := range cases {
		got := explainUpdateError(errString(c.in))
		if !strings.Contains(got, c.wantIn) {
			t.Fatalf("%q → %q,应包含 %q", c.in, got, c.wantIn)
		}
		if c.wantIn != c.in && !strings.Contains(got, c.in) {
			t.Fatalf("原文被吞了: %q", got)
		}
	}
	if explainUpdateError(nil) != "" {
		t.Fatal("nil 应返回空串")
	}
}

// errString 造一个只有 Error() 的错误(避免引 errors 只为一处)。
type errString string

func (e errString) Error() string { return string(e) }

// TestVersionDrift 安装前的版本对账:用户确认的版本必须等于现在要装的版本。
//
// 为什么要有这条(2026-11-03 真机反馈):「0.8.4 里点更新,显示可更新到 0.8.4,实际装上了
// 0.8.5」—— 检查与点安装之间,镜像把清单里的版本换掉了。空串一律不漂移(调用方没传
// expect 时不能凭空拦人)。
// 反向验证:把 versionDrift 改成恒 false,最后一条必红。
func TestVersionDrift(t *testing.T) {
	cases := []struct {
		expect, seen string
		want         bool
	}{
		{"", "", false},
		{"0.8.4", "", false},
		{"", "0.8.5", false},
		{"0.8.4", "0.8.4", false},
		{"0.8.4", "0.8.5", true},
	}
	for _, c := range cases {
		if got := versionDrift(c.expect, c.seen); got != c.want {
			t.Errorf("versionDrift(%q,%q)=%v want %v", c.expect, c.seen, got, c.want)
		}
	}
}

// TestUpdateStateVersion 快照里的 version 该报谁。
//
// 回归自 2026-11-06 真机反馈:0.8.5 上检查更新,按钮先显示「升级到 0.8.8-preview」,
// 一秒后被设置面板的轮询覆盖成「升级到 0.8.5」—— 因为 /shell/update-state 不管状态
// 一律报壳自己的当前版本。反向验证:把 updateStateVersion 改成恒返回 current,本用例转红。
func TestUpdateStateVersion(t *testing.T) {
	cases := []struct {
		state, current, seen, want string
		why                        string
	}{
		{"available", "0.8.5", "0.8.8-preview", "0.8.8-preview", "可更新时必须是远端版本"},
		{"available", "0.8.5", "", "0.8.5", "没记到远端版本:不能凭空造一个"},
		{"upToDate", "0.8.5", "0.8.8-preview", "0.8.5", "已最新时前端只用它显示当前版本"},
		{"checking", "0.8.5", "", "0.8.5", "检查中同上"},
		{"unconfigured", "", "", "", "更新器没装配:如实留空"},
	}
	for _, c := range cases {
		if got := updateStateVersion(c.state, c.current, c.seen); got != c.want {
			t.Errorf("updateStateVersion(%q,%q,%q)=%q want %q(%s)", c.state, c.current, c.seen, got, c.want, c.why)
		}
	}
}

// TestSeenVersion 记录/读取最近一次检查到的远端版本(空串不覆盖),并发安全。
func TestSeenVersion(t *testing.T) {
	sh := &shell{}
	if got := sh.seenVersion(); got != "" {
		t.Fatalf("初始应为空,得到 %q", got)
	}
	sh.noteSeenVersion("0.8.4")
	if got := sh.seenVersion(); got != "0.8.4" {
		t.Fatalf("应记下 0.8.4,得到 %q", got)
	}
	sh.noteSeenVersion("") // 空串不覆盖已有值(没检查到东西不代表"版本变空了")
	if got := sh.seenVersion(); got != "0.8.4" {
		t.Fatalf("空串不该覆盖,得到 %q", got)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sh.noteSeenVersion("0.8.4")
			_ = sh.seenVersion()
		}(i)
	}
	wg.Wait()
}
