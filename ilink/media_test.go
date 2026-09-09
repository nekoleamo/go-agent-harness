// ilink 媒体 helper 单测:下载上限/解密回环(hex+base64 key)/宽容降级/扩展名。
package ilink

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// padPKCS7 手写 PKCS7 填充(测试构造密文用)。
func padPKCS7(data []byte) []byte {
	n := aes.BlockSize - len(data)%aes.BlockSize
	return append(data, bytes.Repeat([]byte{byte(n)}, n)...)
}

// TestDecryptMediaRoundtrip AES-128-ECB 解密:hex 与 base64 两种 key 编码均可解析。
func TestDecryptMediaRoundtrip(t *testing.T) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	plain := []byte("iLink 媒体内容 for decryption test 测试解密 0123456789")
	cipherData := make([]byte, 0, len(plain)+16)
	block, _ := aes.NewCipher(key)
	padded := padPKCS7(plain)
	tmp := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Encrypt(tmp[i:i+16], padded[i:i+16])
	}
	cipherData = tmp
	for _, enc := range []string{"hex", "base64"} {
		var keyStr string
		if enc == "hex" {
			keyStr = hex.EncodeToString(key)
		} else {
			keyStr = base64.StdEncoding.EncodeToString(key)
		}
		got, err := DecryptMedia(cipherData, keyStr)
		if err != nil {
			t.Fatalf("%s key 解密失败: %v", enc, err)
		}
		if string(got) != string(plain) {
			t.Fatalf("%s key 解密不符: %q", enc, got)
		}
	}
}

// TestDecryptMediaBadKey 坏 key/非 16 对齐密文 → 宽容返回原文(不 panic、不丢内容)。
func TestDecryptMediaBadKey(t *testing.T) {
	data := []byte("not-aligned-or-encrypted-data")
	if got, err := DecryptMedia(data, "bad-key"); err == nil || !bytes.Equal(got, data) {
		t.Fatalf("坏 key 应返回原文+错误,got %v err=%v", got, err)
	}
	// 空 key = 无加密直接原样
	if got, err := DecryptMedia(data, ""); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("空 key 应原样返回: %v err=%v", got, err)
	}
	// 对齐但 key 错:解密成功但乱码(尾部 PKCS7 校验宽容)不 panic
	key := strings.Repeat("k", 16)
	block, _ := aes.NewCipher([]byte(key))
	enc := make([]byte, 32)
	block.Encrypt(enc[:16], enc[:16])
	block.Encrypt(enc[16:], enc[16:])
	if _, err := DecryptMedia(enc, strings.Repeat("x", 32)); err == nil {
		// 32 字符 hex 不构成 16 字节(32/2=16 字节?hex 32 字符 = 16 字节,合法 key)
		t.Log("32-hex key 被解析为合法密钥")
	}
	_ = enc
}

// TestDownloadMedia 下载:httptest server 成功/超限拒绝。
func TestDownloadMedia(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4096)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Write(bytes.Repeat([]byte("b"), 21<<20)) // 超 20MB
		case "/ok":
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer hs.Close()

	got, err := DownloadMedia(context.Background(), hs.URL+"/ok")
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("下载不符: %v err=%v", len(got), err)
	}
	if _, err := DownloadMedia(context.Background(), hs.URL+"/big"); err == nil {
		t.Fatal("超限媒体应拒绝")
	}
	if _, err := DownloadMedia(context.Background(), hs.URL+"/missing"); err == nil {
		t.Fatal("404 应报错")
	}
}

// TestExtFromURL 扩展名猜测:query/路径参数剥离;无扩展/非法返回空。
func TestExtFromURL(t *testing.T) {
	cases := map[string]string{
		"https://cdn.iLink.example/a.jpg?x=1&sig=2":     "jpg",
		"https://cdn.iLink.example/path/to/photo.PNG":    "png",
		"https://cdn.iLink.example/video.mp4?token=abc":  "mp4",
		"https://cdn.iLink.example/noext?t=1":            "",
		"https://cdn.iLink.example/evil.file/traversal/": "",
	}
	for u, want := range cases {
		if got := ExtFromURL(u); got != want {
			t.Errorf("ExtFromURL(%q) = %q,want %q", u, got, want)
		}
	}
}
