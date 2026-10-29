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
	"testing"

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

	p := &desktopProvider{client: &http.Client{}}
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
	p := &desktopProvider{client: &http.Client{}}
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
		p := &desktopProvider{client: &http.Client{}}
		if _, err := p.Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.1.6", Platform: "testos", Arch: "testarch"}); err == nil {
			t.Fatalf("坏清单应报错: %s", body)
		}
		ms.Close()
	}
}
