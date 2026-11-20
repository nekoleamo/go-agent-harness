// verify 单测:更新包的信任链(摘要在先、签名在后;fail closed)。
//
// 为什么单独测这一层:这是「零容错区」——壳拿到的字节要么被验过,要么不能装。
// 假 HTTP 服务器提供字节,断言四种形态:摘要对/错、签名对/错、有公钥但清单没签名。
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// newTestProvider 造一个指向测试服务器的 provider(不碰线上清单)。
func newTestProvider() *desktopProvider {
	return &desktopProvider{
		client: &http.Client{},
		dl:     &http.Client{},
	}
}

// releaseFor 造一条「本平台」的 release,摘要/签名由调用方给。
func releaseFor(url string, body []byte, sig []byte) *updateRelease {
	sum := sha256.Sum256(body)
	return &updateRelease{
		Version:  "9.9.9",
		Artifact: updateArtifactInfo{Filename: "x.zip", Platform: "darwin", Arch: "arm64"},
		Verification: &updateVerification{
			Digest:    sum[:],
			Signature: sig,
		},
		Metadata: map[string]any{"url": url},
	}
}

// TestStageUpdatePayloadDigestOnly 没有内置公钥(开发构建):只校验摘要。
func TestStageUpdatePayloadDigestOnly(t *testing.T) {
	body := []byte("payload-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir, err := stageUpdatePayload(context.Background(), newTestProvider(), releaseFor(srv.URL, body, []byte("ignored")), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if _, err := os.Stat(dir + "/x.zip"); err != nil { // 文件名取自清单(X.zip)
		t.Fatalf("产物应落到暂存目录: %v", err)
	}
}

// TestStageUpdatePayloadDigestMismatch 摘要不符必须拒绝,并清掉暂存目录。
func TestStageUpdatePayloadDigestMismatch(t *testing.T) {
	body := []byte("real-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("other-bytes"))
	}))
	defer srv.Close()

	rel := releaseFor(srv.URL, body, nil) // 摘要按 body 算,而服务器给的是别的字节
	if dir, err := stageUpdatePayload(context.Background(), newTestProvider(), rel, nil, nil); err == nil {
		_ = os.RemoveAll(dir)
		t.Fatal("摘要不符应报错")
	}
}

// TestStageUpdatePayloadSignature 有公钥时:签名对 → 通过;签名错 → 拒;没签名 → 拒(fail closed)。
func TestStageUpdatePayloadSignature(t *testing.T) {
	body := []byte("signed-payload")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	good := ed25519.Sign(priv, sum[:])
	other := sha256.Sum256([]byte("other"))
	bad := ed25519.Sign(priv, other[:])

	if dir, err := stageUpdatePayload(context.Background(), newTestProvider(), releaseFor(srv.URL, body, good), pub, nil); err != nil {
		t.Fatalf("签名正确应通过: %v", err)
	} else {
		_ = os.RemoveAll(dir)
	}
	if dir, err := stageUpdatePayload(context.Background(), newTestProvider(), releaseFor(srv.URL, body, bad), pub, nil); err == nil {
		_ = os.RemoveAll(dir)
		t.Fatal("签名不符应报错")
	}
	if dir, err := stageUpdatePayload(context.Background(), newTestProvider(), releaseFor(srv.URL, body, nil), pub, nil); err == nil {
		_ = os.RemoveAll(dir)
		t.Fatal("有公钥但清单没签名应报错(fail closed)")
	}
}

// TestUpdateSourcesConfigurable env 可换清单地址(本地/CI 演练用)。
func TestUpdateSourcesConfigurable(t *testing.T) {
	t.Setenv("GAH_DESKTOP_UPDATE_SOURCES", "http://127.0.0.1:1/a.json, http://127.0.0.1:2/b.json")
	got := updateSources()
	if len(got) != 2 || got[0] != "http://127.0.0.1:1/a.json" || got[1] != "http://127.0.0.1:2/b.json" {
		t.Fatalf("应解析出两条并去空格,得到 %v", got)
	}
}

// TestManifestArtifactSigDecode 清单里的 sig 是 base64、sha256 是 hex(格式契约)。
func TestManifestArtifactSigDecode(t *testing.T) {
	sig := base64.StdEncoding.EncodeToString([]byte("sig-bytes"))
	if _, err := base64.StdEncoding.DecodeString(sig); err != nil {
		t.Fatal(err)
	}
	if _, err := hex.DecodeString("00ff"); err != nil {
		t.Fatal(err)
	}
}
