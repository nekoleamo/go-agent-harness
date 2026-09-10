// 出站媒体(MED-3,beta)协议单测:PKCS7/密文大小、随机密钥形状、AES 回环、
// 三段式端点形状(鉴权头/字段/查询参数/响应头/CSS 重试策略)、媒体项 JSON。
package ilink

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPaddedSize(t *testing.T) {
	cases := map[int64]int64{0: 16, 1: 16, 15: 16, 16: 32, 17: 32, 31: 32, 32: 48}
	for raw, want := range cases {
		if got := PaddedSize(raw); got != want {
			t.Fatalf("PaddedSize(%d)=%d want %d", raw, got, want)
		}
	}
	if got := PaddedSize(-5); got != 16 {
		t.Fatalf("负数应按 0 处理: %d", got)
	}
}

func TestEncryptMediaRoundTrip(t *testing.T) {
	keyHex, err := NewAESKey()
	if err != nil {
		t.Fatal(err)
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 1, 15, 16, 17, 255, 4096} {
		plain := make([]byte, n)
		for i := range plain {
			plain[i] = byte(i % 251)
		}
		cipher, err := EncryptMedia(plain, key)
		if err != nil {
			t.Fatalf("n=%d 加密失败: %v", n, err)
		}
		if int64(len(cipher)) != PaddedSize(int64(n)) {
			t.Fatalf("n=%d 密文长度 %d != PaddedSize %d", n, len(cipher), PaddedSize(int64(n)))
		}
		back, err := DecryptMedia(cipher, keyHex)
		if err != nil {
			t.Fatalf("n=%d 解密失败: %v", n, err)
		}
		if string(back) != string(plain) {
			t.Fatalf("n=%d 回环不一致", n)
		}
	}
	// 密钥长度错误 → 显式报错
	if _, err := EncryptMedia([]byte("x"), []byte("short")); err == nil {
		t.Fatal("非 16 字节密钥应报错")
	}
}

func TestRandomKeysShape(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range []func() (string, error){NewFileKey, NewAESKey} {
		a, err := f()
		if err != nil {
			t.Fatal(err)
		}
		b, err := f()
		if err != nil {
			t.Fatal(err)
		}
		if len(a) != 32 || len(b) != 32 {
			t.Fatalf("应为 32 字符 hex: %q %q", a, b)
		}
		if raw, err := hex.DecodeString(a); err != nil || len(raw) != 16 {
			t.Fatalf("应解码为 16 字节: %q %v", a, err)
		}
		if a == b {
			t.Fatalf("两次随机应不同: %q", a)
		}
		seen[a] = true
	}
	if len(seen) != 2 {
		t.Fatal("filekey 与 aeskey 不应共享生成序列")
	}
}

func TestMediaKeyB64(t *testing.T) {
	keyHex := "00112233445566778899aabbccddeeff"
	want := base64.StdEncoding.EncodeToString([]byte(keyHex))
	if got := MediaKeyB64(keyHex); got != want {
		t.Fatalf("aes_key 应为 base64(hex 字符串): %q != %q", got, want)
	}
}

func TestGetUploadURLShape(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/getuploadurl" {
			t.Errorf("路径不符: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("应 POST: %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer tk-1" || r.Header.Get("AuthorizationType") != "ilink_bot_token" {
			t.Errorf("鉴权头不符: %v", r.Header)
		}
		if r.Header.Get("X-WECHAT-UIN") == "" {
			t.Error("缺少 X-WECHAT-UIN")
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type 不符: %q", ct)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"ret":0,"upload_param":"up-1","thumb_upload_param":"tp-1"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tk-1")
	got, err := c.GetUploadURL(context.Background(), UploadRequest{
		FileKey: "fk-1", MediaType: MediaTypeFile, ToUserID: "u1",
		RawSize: 5, RawFileMD5: "md5hex", FileSize: 16, AESKey: "ak-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.UploadParam != "up-1" || got.ThumbUploadParam != "tp-1" {
		t.Fatalf("响应解析不符: %+v", got)
	}
	for k, want := range map[string]any{
		"filekey": "fk-1", "media_type": float64(MediaTypeFile), "to_user_id": "u1",
		"rawsize": float64(5), "rawfilemd5": "md5hex", "filesize": float64(16),
		"aeskey": "ak-1", "no_need_thumb": true,
	} {
		if body[k] != want {
			t.Fatalf("请求字段 %s 不符: %v", k, body[k])
		}
	}
	bi, _ := body["base_info"].(map[string]any)
	if bi == nil || bi["channel_version"] != ChannelVersion {
		t.Fatalf("base_info 缺失/版本不符: %v", body["base_info"])
	}
}

func TestGetUploadURLFullURLAndEmpty(t *testing.T) {
	// v2.1+ 只回 upload_full_url → 兼容
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0,"upload_full_url":"https://cdn.example/up?a=1"}`))
	}))
	defer srv.Close()
	got, err := New(srv.URL, "tk").GetUploadURL(context.Background(), UploadRequest{})
	if err != nil || got.UploadFullURL != "https://cdn.example/up?a=1" {
		t.Fatalf("upload_full_url 兼容失败: %+v %v", got, err)
	}
	// 两者皆空 → 显式报错(不静默)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer srv2.Close()
	if _, err := New(srv2.URL, "tk").GetUploadURL(context.Background(), UploadRequest{}); err == nil {
		t.Fatal("无上传参数应报错")
	}
}

func TestGetUploadURLSessionExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret":-14,"errmsg":"session expired"}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "tk").GetUploadURL(context.Background(), UploadRequest{})
	if err == nil || !SessionExpired(err) {
		t.Fatalf("ret=-14 应判定会话过期: %v", err)
	}
}

func TestUploadToCDN(t *testing.T) {
	cipher := []byte("CIPHERTEXT-16B!!")
	var gotQuery url.Values
	var gotBody []byte
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("x-encrypted-param", "enc-xyz")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := New("https://api.example", "tk")
	c.CDNURL = srv.URL + "/c2c/upload"
	param, err := c.UploadToCDN(context.Background(), &UploadTicket{UploadParam: "up-1"}, "fk-1", cipher)
	if err != nil {
		t.Fatal(err)
	}
	if param != "enc-xyz" {
		t.Fatalf("应返回 x-encrypted-param: %q", param)
	}
	if gotQuery.Get("encrypted_query_param") != "up-1" || gotQuery.Get("filekey") != "fk-1" {
		t.Fatalf("查询参数不符: %v", gotQuery)
	}
	if gotCT != "application/octet-stream" {
		t.Fatalf("Content-Type 不符: %q", gotCT)
	}
	if string(gotBody) != string(cipher) {
		t.Fatalf("body 应为密文: %q", gotBody)
	}
	// upload_full_url 优先且原样使用(不追加 filekey)
	var fullQuery url.Values
	var fullPath string
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fullQuery, fullPath = r.URL.Query(), r.URL.Path
		w.Header().Set("x-encrypted-param", "enc-full")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv2.Close()
	c2 := New("https://api.example", "tk") // CDNURL 不设 → 走 full url
	got, err := c2.UploadToCDN(context.Background(), &UploadTicket{UploadFullURL: srv2.URL + "/full?token=t1"}, "fk-9", cipher)
	if err != nil || got != "enc-full" {
		t.Fatalf("upload_full_url 路径失败: %q %v", got, err)
	}
	if fullPath != "/full" || fullQuery.Get("token") != "t1" || fullQuery.Get("filekey") != "" {
		t.Fatalf("full url 应原样使用: %s %v", fullPath, fullQuery)
	}
	// 缺 x-encrypted-param → 显式报错
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv3.Close()
	c3 := New("https://api.example", "tk")
	c3.CDNURL = srv3.URL + "/c2c/upload"
	if _, err := c3.UploadToCDN(context.Background(), &UploadTicket{UploadParam: "up"}, "fk", cipher); err == nil ||
		!strings.Contains(err.Error(), "x-encrypted-param") {
		t.Fatalf("缺响应头应报错: %v", err)
	}
	// 缺上传参数 → 报错
	if _, err := c3.UploadToCDN(context.Background(), nil, "fk", cipher); err == nil {
		t.Fatal("缺 ticket 应报错")
	}
}

func TestUploadToCDNRetryPolicy(t *testing.T) {
	// 5xx → 重试 ≤3 次后成功
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("x-encrypted-param", "enc-after-retry")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := New("https://api.example", "tk")
	c.CDNURL = srv.URL + "/c2c/upload"
	got, err := c.UploadToCDN(context.Background(), &UploadTicket{UploadParam: "up"}, "fk", []byte("c"))
	if err != nil || got != "enc-after-retry" {
		t.Fatalf("5xx 应重试后成功: %q %v", got, err)
	}
	if n := atomic.LoadInt32(&attempts); n != 3 {
		t.Fatalf("应尝试 3 次: %d", n)
	}
	// 持续 5xx → 3 次后失败(不无限重试)
	var always int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&always, 1)
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv2.Close()
	c2 := New("https://api.example", "tk")
	c2.CDNURL = srv2.URL + "/c2c/upload"
	if _, err := c2.UploadToCDN(context.Background(), &UploadTicket{UploadParam: "up"}, "fk", []byte("c")); err == nil {
		t.Fatal("持续 5xx 应报错")
	}
	if n := atomic.LoadInt32(&always); n != cdnRetryMax {
		t.Fatalf("应恰好尝试 %d 次: %d", cdnRetryMax, n)
	}
	// 4xx → 立即中止(不重试)
	var c4 int32
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&c4, 1)
		http.Error(w, "bad param", http.StatusBadRequest)
	}))
	defer srv3.Close()
	c3 := New("https://api.example", "tk")
	c3.CDNURL = srv3.URL + "/c2c/upload"
	if _, err := c3.UploadToCDN(context.Background(), &UploadTicket{UploadParam: "up"}, "fk", []byte("c")); err == nil {
		t.Fatal("4xx 应报错")
	}
	if n := atomic.LoadInt32(&c4); n != 1 {
		t.Fatalf("4xx 不应重试: %d", n)
	}
}

func TestMediaItemPayload(t *testing.T) {
	keyHex := "00112233445566778899aabbccddeeff"
	aesB64 := base64.StdEncoding.EncodeToString([]byte(keyHex))
	base := MediaItem{EncryptQueryParam: "enc-1", AESKeyHex: keyHex, EncryptType: 1}

	img := base
	img.Type, img.Size = ItemTypeImage, 32
	p, err := img.payload()
	if err != nil {
		t.Fatal(err)
	}
	ii, _ := p["image_item"].(map[string]any)
	media, _ := ii["media"].(map[string]any)
	if p["type"] != ItemTypeImage || ii["mid_size"] != int64(32) || media["aes_key"] != aesB64 ||
		media["encrypt_type"] != 1 || media["encrypt_query_param"] != "enc-1" {
		t.Fatalf("图片项 payload 不符: %v", p)
	}

	file := base
	file.Type, file.FileName, file.MD5, file.RawSize = ItemTypeFile, "a.zip", "md5x", 1234
	p, err = file.payload()
	if err != nil {
		t.Fatal(err)
	}
	fitem, _ := p["file_item"].(map[string]any)
	if p["type"] != ItemTypeFile || fitem["file_name"] != "a.zip" || fitem["md5"] != "md5x" || fitem["len"] != "1234" {
		t.Fatalf("文件项 payload 不符: %v", p)
	}
	if _, ok := fitem["media"].(map[string]any); !ok {
		t.Fatalf("文件项应含 media: %v", fitem)
	}

	video := base
	video.Type, video.Size, video.PlayLength = ItemTypeVideo, 64, 3000
	p, err = video.payload()
	if err != nil {
		t.Fatal(err)
	}
	vitem, _ := p["video_item"].(map[string]any)
	if p["type"] != ItemTypeVideo || vitem["video_size"] != int64(64) || vitem["play_length"] != 3000 {
		t.Fatalf("视频项 payload 不符: %v", p)
	}

	voice := base
	voice.Type = ItemTypeVoice
	p, err = voice.payload()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p["voice_item"].(map[string]any); !ok {
		t.Fatalf("语音项 payload 不符: %v", p)
	}

	// 不支持的 type / 缺 encrypt_query_param → 显式报错
	bad := base
	bad.Type = ItemTypeText
	if _, err := bad.payload(); err == nil {
		t.Fatal("非媒体类型应报错")
	}
	noParam := base
	noParam.Type = ItemTypeImage
	noParam.EncryptQueryParam = ""
	if _, err := noParam.payload(); err == nil {
		t.Fatal("缺 encrypt_query_param 应报错")
	}
}

func TestSendMediaMessageShape(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			t.Errorf("路径不符: %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tk")
	item := MediaItem{Type: ItemTypeImage, EncryptQueryParam: "enc-1", AESKeyHex: "aabb", EncryptType: 1, Size: 16}
	if err := c.SendMediaMessage(context.Background(), "u1", "ctx-1", "cid-1", item); err != nil {
		t.Fatal(err)
	}
	msg, _ := body["msg"].(map[string]any)
	if msg == nil || msg["to_user_id"] != "u1" || msg["context_token"] != "ctx-1" ||
		msg["client_id"] != "cid-1" || msg["message_type"] != float64(2) || msg["message_state"] != float64(2) {
		t.Fatalf("sendmessage msg 不符: %v", body)
	}
	list, _ := msg["item_list"].([]any)
	if len(list) != 1 {
		t.Fatalf("item_list 应单元素: %v", msg)
	}
	first, _ := list[0].(map[string]any)
	if first["type"] != float64(ItemTypeImage) {
		t.Fatalf("媒体项 type 不符: %v", first)
	}
	bi, _ := body["base_info"].(map[string]any)
	if bi == nil || bi["channel_version"] != ChannelVersion {
		t.Fatalf("base_info 缺失: %v", body)
	}
	// 缺 context_token → 显式报错(微信无主动推送)
	if err := c.SendMediaMessage(context.Background(), "u1", "", "", item); err == nil ||
		!strings.Contains(err.Error(), "context_token") {
		t.Fatalf("缺 token 应报错: %v", err)
	}
	// 未登录 → ErrNotLoggedIn
	c2 := New(srv.URL, "")
	if err := c2.SendMediaMessage(context.Background(), "u1", "ctx", "", item); err != ErrNotLoggedIn {
		t.Fatalf("未登录应返回 ErrNotLoggedIn: %v", err)
	}
	// 非法媒体项 → 发送前即报错
	bad := MediaItem{Type: ItemTypeText, EncryptQueryParam: "enc"}
	if err := c.SendMediaMessage(context.Background(), "u1", "ctx", "", bad); err == nil {
		t.Fatal("非法媒体项应报错")
	}
	// 空 clientID → 自动生成(可发送)
	if err := c.SendMediaMessage(context.Background(), "u1", "ctx", "", item); err != nil {
		t.Fatal(err)
	}
}
