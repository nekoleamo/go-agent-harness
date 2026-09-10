// 出站媒体(MED-3,beta)单测:三段式形状(①参数申请 ②加密上传 ③媒体项)/
// 类型映射与降级 / 守卫(群、未登记、无 token、大小不符、超限)/ 失败显式可见 /
// 真实客户端全链路(httptest 假 CDN)。零外网。
package uimwechat

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/ilink"
	"github.com/nekoleamo/go-agent-harness/im"
)

// fakeMediaAPI 记录三段式调用(可编程失败)。
type fakeMediaAPI struct {
	reqs     []ilink.UploadRequest
	tickets  []*ilink.UploadTicket
	fileKeys []string
	payloads [][]byte
	items    []ilink.MediaItem
	sendTo   []string
	sendTok  []string

	getErr    error
	uploadErr error
	sendErr   error
}

func (f *fakeMediaAPI) GetUploadURL(_ context.Context, req ilink.UploadRequest) (*ilink.UploadTicket, error) {
	f.reqs = append(f.reqs, req)
	if f.getErr != nil {
		return nil, f.getErr
	}
	t := &ilink.UploadTicket{UploadParam: "up-1"}
	f.tickets = append(f.tickets, t)
	return t, nil
}

func (f *fakeMediaAPI) UploadToCDN(_ context.Context, _ *ilink.UploadTicket, fileKey string, ciphertext []byte) (string, error) {
	f.fileKeys = append(f.fileKeys, fileKey)
	f.payloads = append(f.payloads, ciphertext)
	if f.uploadErr != nil {
		return "", f.uploadErr
	}
	return "enc-1", nil
}

func (f *fakeMediaAPI) SendMediaMessage(_ context.Context, to, token, _ string, item ilink.MediaItem) error {
	f.sendTo = append(f.sendTo, to)
	f.sendTok = append(f.sendTok, token)
	f.items = append(f.items, item)
	return f.sendErr
}

// mediaTransportW 构造待测 transport(fake 客户端 + u1 的 context_token)+ 一份登记产物。
func mediaTransportW(t *testing.T, api wechatMediaAPI, kind im.MediaKind, name string) (*wechatTransport, im.MediaPayload) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	data := []byte("PAYLOAD-媒体字节")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	tr := &wechatTransport{name: channelName, tokens: map[string]string{"u1": "ctx-1"}, mediaAPIOverride: api}
	return tr, im.MediaPayload{
		ArtifactID: "art-1", Kind: kind, Path: p, Name: name,
		Size: fi.Size(), TimeUnixNano: fi.ModTime().UnixNano(),
	}
}

func TestMediaTypeForKind(t *testing.T) {
	cases := []struct {
		kind                  im.MediaKind
		name                  string
		wantMedia, wantItem   int
		wantDowngradeNonEmpty bool
	}{
		{im.MediaImage, "a.png", ilink.MediaTypeImage, ilink.ItemTypeImage, false},
		{im.MediaImage, "a.JPG", ilink.MediaTypeImage, ilink.ItemTypeImage, false},
		{im.MediaImage, "a.gif", ilink.MediaTypeImage, ilink.ItemTypeImage, false},
		{im.MediaImage, "a.tiff", ilink.MediaTypeFile, ilink.ItemTypeFile, true},
		{im.MediaVideo, "a.mp4", ilink.MediaTypeVideo, ilink.ItemTypeVideo, false},
		{im.MediaVideo, "a.mov", ilink.MediaTypeFile, ilink.ItemTypeFile, true},
		{im.MediaVoice, "a.mp3", ilink.MediaTypeFile, ilink.ItemTypeFile, true},
		{im.MediaFile, "a.zip", ilink.MediaTypeFile, ilink.ItemTypeFile, false},
	}
	for _, c := range cases {
		mt, it, note := mediaTypeForKind(c.kind, c.name)
		if mt != c.wantMedia || it != c.wantItem {
			t.Fatalf("mediaTypeForKind(%s,%s)=(%d,%d) want (%d,%d)", c.kind, c.name, mt, it, c.wantMedia, c.wantItem)
		}
		if (note != "") != c.wantDowngradeNonEmpty {
			t.Fatalf("降级说明不符(%s,%s): %q", c.kind, c.name, note)
		}
	}
}

func TestSendMediaImageThreeStageShape(t *testing.T) {
	api := &fakeMediaAPI{}
	tr, m := mediaTransportW(t, api, im.MediaImage, "chart.png")
	plain, err := os.ReadFile(m.Path)
	if err != nil {
		t.Fatal(err)
	}
	route := im.Route{Channel: channelName, UserID: "u1", ChatID: "u1"}
	if err := tr.SendMedia(context.Background(), route, m); err != nil {
		t.Fatalf("发送应成功: %v", err)
	}
	// ① getuploadurl:字段与派生量口径
	if len(api.reqs) != 1 {
		t.Fatalf("应申请一次上传参数: %d", len(api.reqs))
	}
	req := api.reqs[0]
	sum := md5.Sum(plain)
	if req.MediaType != ilink.MediaTypeImage || req.ToUserID != "u1" {
		t.Fatalf("media_type/to_user_id 不符: %+v", req)
	}
	if req.RawSize != int64(len(plain)) || req.FileSize != ilink.PaddedSize(int64(len(plain))) {
		t.Fatalf("rawsize/filesize 不符: %+v(padded=%d)", req, ilink.PaddedSize(int64(len(plain))))
	}
	if req.RawFileMD5 != hex.EncodeToString(sum[:]) {
		t.Fatalf("rawfilemd5 应为明文 MD5: %+v", req)
	}
	for name, v := range map[string]string{"filekey": req.FileKey, "aeskey": req.AESKey} {
		if len(v) != 32 {
			t.Fatalf("%s 应为 16 字节 hex(32 字符): %q", name, v)
		}
		if _, err := hex.DecodeString(v); err != nil {
			t.Fatalf("%s 应为 hex: %q", name, v)
		}
	}
	// ② CDN:密文可解回明文,且用同一 filekey
	if len(api.payloads) != 1 || api.fileKeys[0] != req.FileKey {
		t.Fatalf("CDN 上传应使用申请时的 filekey: %v vs %s", api.fileKeys, req.FileKey)
	}
	if int64(len(api.payloads[0])) != req.FileSize {
		t.Fatalf("密文长度应为 filesize(%d): %d", req.FileSize, len(api.payloads[0]))
	}
	back, err := ilink.DecryptMedia(api.payloads[0], req.AESKey)
	if err != nil {
		t.Fatalf("密文应可解密: %v", err)
	}
	if string(back) != string(plain) {
		t.Fatalf("解密回环不一致: %q", back)
	}
	// ③ 媒体项:图片项形状
	if len(api.items) != 1 || api.sendTo[0] != "u1" || api.sendTok[0] != "ctx-1" {
		t.Fatalf("媒体项发送参数不符: %+v to=%v tok=%v", api.items, api.sendTo, api.sendTok)
	}
	it := api.items[0]
	if it.Type != ilink.ItemTypeImage || it.EncryptQueryParam != "enc-1" ||
		it.Size != int64(len(api.payloads[0])) || it.AESKeyHex != req.AESKey || it.EncryptType != 1 {
		t.Fatalf("图片项形状不符: %+v", it)
	}
	tr.mu.Lock()
	le := tr.lastError
	tr.mu.Unlock()
	if strings.Contains(le, "降级") {
		t.Fatalf("图片正常路径不应有降级提示: %q", le)
	}
}

func TestSendMediaFileItemAndDowngrade(t *testing.T) {
	// 文件项:file_name/md5/len(明文字符串)
	api := &fakeMediaAPI{}
	tr, m := mediaTransportW(t, api, im.MediaFile, "报告.zip")
	if err := tr.SendMedia(context.Background(), im.Route{Channel: channelName, UserID: "u1", ChatID: "u1"}, m); err != nil {
		t.Fatal(err)
	}
	it := api.items[0]
	if it.Type != ilink.ItemTypeFile || it.FileName != "报告.zip" || it.RawSize != m.Size ||
		it.MD5 != api.reqs[0].RawFileMD5 {
		t.Fatalf("文件项形状不符: %+v", it)
	}
	// 降级:image 语义但扩展名不在图片枚举 → 按文件项发,并在 lastError 留痕
	api2 := &fakeMediaAPI{}
	tr2, m2 := mediaTransportW(t, api2, im.MediaImage, "scan.tiff")
	if err := tr2.SendMedia(context.Background(), im.Route{Channel: channelName, UserID: "u1", ChatID: "u1"}, m2); err != nil {
		t.Fatal(err)
	}
	if api2.items[0].Type != ilink.ItemTypeFile {
		t.Fatalf("应降级为文件项: %+v", api2.items[0])
	}
	tr2.mu.Lock()
	le := tr2.lastError
	tr2.mu.Unlock()
	if !strings.Contains(le, "按文件类型发送") {
		t.Fatalf("降级应留痕 lastError: %q", le)
	}
}

func TestSendMediaGuards(t *testing.T) {
	route := im.Route{Channel: channelName, UserID: "u1", ChatID: "u1"}
	ctx := context.Background()
	// 群投递不支持
	api := &fakeMediaAPI{}
	tr, m := mediaTransportW(t, api, im.MediaFile, "a.zip")
	if err := tr.SendMedia(ctx, im.Route{Channel: channelName, UserID: "g1", ChatID: "g1", Group: true}, m); err == nil ||
		!strings.Contains(err.Error(), "不支持群投递") {
		t.Fatalf("群投递应拒绝: %v", err)
	}
	// 未登记产物
	m0 := m
	m0.ArtifactID = ""
	if err := tr.SendMedia(ctx, route, m0); err == nil || !strings.Contains(err.Error(), "已登记产物") {
		t.Fatalf("未登记产物应拒绝: %v", err)
	}
	// 无 context_token(对方未先发言)
	tr2, m2 := mediaTransportW(t, &fakeMediaAPI{}, im.MediaFile, "a.zip")
	tr2.mu.Lock()
	tr2.tokens = map[string]string{}
	tr2.mu.Unlock()
	if err := tr2.SendMedia(ctx, route, m2); err == nil || !strings.Contains(err.Error(), "context_token") {
		t.Fatalf("无 token 应显式报错: %v", err)
	}
	// 未登录(无客户端且无替身)
	tr3 := &wechatTransport{name: channelName, tokens: map[string]string{"u1": "ctx-1"}}
	if err := tr3.SendMedia(ctx, route, m2); err == nil || !strings.Contains(err.Error(), "未登录") {
		t.Fatalf("未登录应显式报错: %v", err)
	}
	// 路径不可读
	tr4, m4 := mediaTransportW(t, &fakeMediaAPI{}, im.MediaFile, "a.zip")
	m4.Path = filepath.Join(t.TempDir(), "nope.zip")
	if err := tr4.SendMedia(ctx, route, m4); err == nil || !strings.Contains(err.Error(), "不可读") {
		t.Fatalf("不可读应报错: %v", err)
	}
	// 大小与登记不符(登记后被改动)
	tr5, m5 := mediaTransportW(t, &fakeMediaAPI{}, im.MediaFile, "a.zip")
	m5.Size += 3
	if err := tr5.SendMedia(ctx, route, m5); err == nil || !strings.Contains(err.Error(), "与登记不符") {
		t.Fatalf("大小不符应拒绝: %v", err)
	}
	// 超 20MB 上限
	tr6, m6 := mediaTransportW(t, &fakeMediaAPI{}, im.MediaFile, "big.bin")
	if err := os.WriteFile(m6.Path, make([]byte, maxOutMediaBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	m6.Size = int64(maxOutMediaBytes + 1)
	if err := tr6.SendMedia(ctx, route, m6); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("超限应拒绝: %v", err)
	}
}

func TestSendMediaFailuresVisible(t *testing.T) {
	route := im.Route{Channel: channelName, UserID: "u1", ChatID: "u1"}
	ctx := context.Background()
	cases := []struct {
		name    string
		api     *fakeMediaAPI
		wantErr string
		wantLog string
	}{
		{"申请参数失败", &fakeMediaAPI{getErr: errors.New("boom")}, "媒体上传参数申请失败", "申请上传参数失败"},
		{"CDN 上传失败", &fakeMediaAPI{uploadErr: errors.New("cdn 500")}, "CDN 上传失败", "CDN 上传失败"},
		{"媒体消息失败", &fakeMediaAPI{sendErr: errors.New("network down")}, "媒体消息发送失败", ""},
		{"会话过期", &fakeMediaAPI{sendErr: errors.New("ilink: sendmessage error ret=-14 errmsg=\"expired\"")},
			"媒体消息发送失败", "重新扫码登录"},
	}
	for _, c := range cases {
		tr, m := mediaTransportW(t, c.api, im.MediaFile, "a.zip")
		err := tr.SendMedia(ctx, route, m)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Fatalf("%s 应显式报错: %v", c.name, err)
		}
		tr.mu.Lock()
		le := tr.lastError
		tr.mu.Unlock()
		if c.wantLog != "" && !strings.Contains(le, c.wantLog) {
			t.Fatalf("%s 应记录 lastError(%s): %q", c.name, c.wantLog, le)
		}
	}
}

// TestSendMediaFullChainRealClient 全链路:真实 *ilink.Client 打 httptest 假 iLink API + 假 CDN,
// 断言三段式的线上形状(请求路径/鉴权头/查询参数/媒体项 JSON)。
func TestSendMediaFullChainRealClient(t *testing.T) {
	var (
		gotURLReq  map[string]any
		gotCDNBody []byte
		gotCDNQ    url.Values
		gotSend    map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getuploadurl":
			if r.Header.Get("Authorization") != "Bearer tk-1" || r.Header.Get("AuthorizationType") != "ilink_bot_token" {
				t.Errorf("鉴权头不符: %v", r.Header)
			}
			if r.Header.Get("X-WECHAT-UIN") == "" {
				t.Error("缺少 X-WECHAT-UIN")
			}
			_ = json.NewDecoder(r.Body).Decode(&gotURLReq)
			_, _ = w.Write([]byte(`{"ret":0,"upload_param":"up-full"}`))
		case "/c2c/upload":
			gotCDNQ = r.URL.Query()
			if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
				t.Errorf("CDN Content-Type 应为 octet-stream: %q", ct)
			}
			gotCDNBody, _ = io.ReadAll(r.Body)
			w.Header().Set("x-encrypted-param", "enc-full")
			_, _ = w.Write([]byte("ok"))
		case "/ilink/bot/sendmessage":
			_ = json.NewDecoder(r.Body).Decode(&gotSend)
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	api := ilink.New(srv.URL, "tk-1")
	api.CDNURL = srv.URL + "/c2c/upload"
	tr := &wechatTransport{name: channelName, tokens: map[string]string{"u1": "ctx-live"}, client: api}
	dir := t.TempDir()
	p := filepath.Join(dir, "note.txt")
	plain := []byte("hello 出站媒体")
	if err := os.WriteFile(p, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	m := im.MediaPayload{ArtifactID: "art-9", Kind: im.MediaFile, Path: p, Name: "note.txt",
		Size: fi.Size(), TimeUnixNano: fi.ModTime().UnixNano()}
	if err := tr.SendMedia(context.Background(), im.Route{Channel: channelName, UserID: "u1", ChatID: "u1"}, m); err != nil {
		t.Fatalf("全链路应成功: %v", err)
	}
	// ① 线上字段
	if gotURLReq["media_type"] != float64(ilink.MediaTypeFile) || gotURLReq["to_user_id"] != "u1" {
		t.Fatalf("getuploadurl 字段不符: %v", gotURLReq)
	}
	aesKey, _ := gotURLReq["aeskey"].(string)
	fileKey, _ := gotURLReq["filekey"].(string)
	if gotURLReq["no_need_thumb"] != true {
		t.Fatalf("no_need_thumb 应为 true: %v", gotURLReq)
	}
	if gotURLReq["filesize"] != float64(ilink.PaddedSize(int64(len(plain)))) {
		t.Fatalf("filesize 应为 PKCS7 后大小: %v", gotURLReq)
	}
	// ② CDN 查询参数 + 密文可解
	if gotCDNQ.Get("encrypted_query_param") != "up-full" || gotCDNQ.Get("filekey") != fileKey {
		t.Fatalf("CDN 查询参数不符: %v", gotCDNQ)
	}
	back, err := ilink.DecryptMedia(gotCDNBody, aesKey)
	if err != nil || string(back) != string(plain) {
		t.Fatalf("CDN 密文应可解密回明文: %v %q", err, back)
	}
	// ③ 媒体项 JSON(文件项 + aes_key = base64(hex))
	msg, _ := gotSend["msg"].(map[string]any)
	if msg == nil || msg["context_token"] != "ctx-live" || msg["message_type"] != float64(2) {
		t.Fatalf("sendmessage msg 形状不符: %v", gotSend)
	}
	list, _ := msg["item_list"].([]any)
	if len(list) != 1 {
		t.Fatalf("item_list 应单元素: %v", msg)
	}
	item, _ := list[0].(map[string]any)
	if item["type"] != float64(ilink.ItemTypeFile) {
		t.Fatalf("文件项 type 应为 4: %v", item)
	}
	fi2, _ := item["file_item"].(map[string]any)
	if fi2 == nil || fi2["file_name"] != "note.txt" || fi2["len"] != "18" {
		t.Fatalf("file_item 形状不符: %v", item)
	}
	media, _ := fi2["media"].(map[string]any)
	if media == nil || media["encrypt_query_param"] != "enc-full" ||
		media["aes_key"] != base64.StdEncoding.EncodeToString([]byte(aesKey)) || media["encrypt_type"] != float64(1) {
		t.Fatalf("media 形状不符: %v", fi2)
	}
}

// TestWechatTransportImplementsMediaSender 通道契约:未实现 → 桥报「不支持出站文件」。
func TestWechatTransportImplementsMediaSender(t *testing.T) {
	var v any = &wechatTransport{name: channelName}
	if _, ok := v.(im.MediaSender); !ok {
		t.Fatal("wechatTransport 应实现 im.MediaSender(MED-3)")
	}
}
