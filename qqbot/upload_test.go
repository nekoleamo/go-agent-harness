// 富媒体分片上传单测(MED-2):四步流程请求形状 / 校验值 / 端点分派 / 错误分类 / 缓存前提示。
// 全部离线:mock OpenAPI server(含分片 PUT 端点),不触真机。
package qqbot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// mockUpload 分片上传 mock:prepare / part PUT / part_finish / files 四步 + 消息发送。
type mockUpload struct {
	mu sync.Mutex

	prepareBody  map[string]any
	preparePath  string
	prepareCount int
	parts        map[string][]byte // presigned path → 收到的字节
	partAuth     map[string]string
	finishBodies []map[string]any
	filesBody    map[string]any
	filesPath    string
	sendBody     map[string]any

	blockSize   string // prepare 返回的分片大小
	partCount   int    // 返回的分片数
	uploadID    string
	fileInfo    string
	prepareErr  *APIError
	filesErr    *APIError
	putStatus   int // 非 0 时 PUT 返回该状态码
	srv         *httptest.Server
	uploadedMD5 []string
}

func newMockUpload(t *testing.T) (*mockUpload, *httptest.Server, *TokenSource) {
	t.Helper()
	m := &mockUpload{
		parts:     map[string][]byte{},
		partAuth:  map[string]string{},
		blockSize: "5",
		partCount: 2,
		uploadID:  "upload-1",
		fileInfo:  "fi-1",
	}
	mux := http.NewServeMux()
	// token
	mux.HandleFunc("/app/getAppAccessToken", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-up", "expires_in": 7200})
	})
	// 分片 PUT(预签名 URL 指向 /put/{name})
	for i := 0; i < 4; i++ {
		name := "/put/" + string(rune('a'+i))
		mux.HandleFunc(name, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			m.mu.Lock()
			m.parts[r.URL.Path] = body
			m.partAuth[r.URL.Path] = r.Header.Get("Authorization")
			status := m.putStatus
			m.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
	}
	// 四步 REST(单聊 + 群)
	handle := func(suffix string, w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/upload_prepare"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			m.mu.Lock()
			m.prepareBody = body
			m.preparePath = r.URL.Path
			m.prepareCount++
			err := m.prepareErr
			blockSize, partCount, uploadID := m.blockSize, m.partCount, m.uploadID
			m.mu.Unlock()
			if err != nil {
				json.NewEncoder(w).Encode(err)
				return
			}
			parts := make([]map[string]any, 0, partCount)
			for i := 0; i < partCount; i++ {
				parts = append(parts, map[string]any{
					"index":         i, // 官方示例从 0 起
					"presigned_url": m.srv.URL + "/put/" + string(rune('a'+i)),
					"block_size":    blockSize,
				})
			}
			json.NewEncoder(w).Encode(map[string]any{
				"upload_id": uploadID, "block_size": blockSize, "parts": parts,
				"upload_config": map[string]any{"concurrency": 1, "retry_timeout": 300, "retry_delay": 1},
			})
		case strings.HasSuffix(r.URL.Path, "/upload_part_finish"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			m.mu.Lock()
			m.finishBodies = append(m.finishBodies, body)
			m.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{})
		case strings.HasSuffix(r.URL.Path, "/files"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			m.mu.Lock()
			m.filesBody = body
			m.filesPath = r.URL.Path
			err := m.filesErr
			info := m.fileInfo
			m.mu.Unlock()
			if err != nil {
				json.NewEncoder(w).Encode(err)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"file_uuid": "uuid-1", "file_info": info, "ttl": 3600,
			})
		case strings.HasSuffix(r.URL.Path, "/messages"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			m.mu.Lock()
			m.sendBody = body
			m.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"id": "msg-1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
		_ = suffix
	}
	mux.HandleFunc("/v2/users/", func(w http.ResponseWriter, r *http.Request) { handle("user", w, r) })
	mux.HandleFunc("/v2/groups/", func(w http.ResponseWriter, r *http.Request) { handle("group", w, r) })

	hs := httptest.NewServer(mux)
	m.srv = hs
	t.Cleanup(hs.Close)
	ts := NewTokenSource("app-1", "sec-1").WithHTTP(hs.Client())
	ts.URL = hs.URL + "/app/getAppAccessToken"
	return m, hs, ts
}

func TestUploadLocalFileFourSteps(t *testing.T) {
	m, hs, ts := newMockUpload(t)
	c := NewClient(ts).WithBaseURL(hs.URL)
	// 12 字节 + block_size=5 → 官方应返回 3 片(5/5/2);验证偏移累计与末片长度
	m.partCount = 3
	data := []byte("0123456789ab")

	out, err := c.UploadLocalFile(context.Background(), false, "openid-1", FileTypeFile, "报告.pdf", data)
	if err != nil {
		t.Fatalf("上传应成功: %v", err)
	}
	if out.FileInfo != "fi-1" || out.FileUUID != "uuid-1" || out.Bytes != int64(len(data)) {
		t.Fatalf("上传结果异常: %+v", out)
	}
	if got := out.ExpiresIn(); got != 3600*1e9 {
		t.Fatalf("TTL 应归一为秒级时长: %d", got)
	}

	// ① prepare:字段名与类型(官方要求 file_size 为字符串 + md5/sha1/md5_10m)
	pb := m.prepareBody
	if pb["file_type"] != float64(FileTypeFile) || pb["file_name"] != "报告.pdf" {
		t.Fatalf("prepare 字段异常: %+v", pb)
	}
	if pb["file_size"] != "12" {
		t.Fatalf("file_size 应为字符串 \"12\": %#v", pb["file_size"])
	}
	if pb["md5"] != hexMD5(data) || pb["sha1"] != hexSHA1(data) || pb["md5_10m"] != hexMD5(data) {
		t.Fatalf("校验值异常: %+v", pb)
	}
	if m.preparePath != "/v2/users/openid-1/upload_prepare" {
		t.Fatalf("prepare 路径应为单聊: %s", m.preparePath)
	}

	// ② 分片 PUT:预签名 URL 上**不得带鉴权头**;内容按 block_size 顺序拼回原文
	if len(m.parts) != 3 {
		t.Fatalf("应有 3 个分片: %+v", len(m.parts))
	}
	var joined []byte
	for i := 0; i < 3; i++ {
		p := "/put/" + string(rune('a'+i))
		if auth := m.partAuth[p]; auth != "" {
			t.Fatalf("分片 PUT 不应带鉴权头,得 %q", auth)
		}
		joined = append(joined, m.parts[p]...)
	}
	if string(joined) != string(data) {
		t.Fatalf("分片内容拼回不符: %q", joined)
	}

	// ③ part_finish:每片一次,回传 prepare 的 index 与 block_size + 该片 md5
	if len(m.finishBodies) != 3 {
		t.Fatalf("应确认 3 片: %d", len(m.finishBodies))
	}
	first := m.finishBodies[0]
	if first["upload_id"] != "upload-1" || first["part_index"] != float64(0) || first["block_size"] != "5" {
		t.Fatalf("part_finish 字段异常: %+v", first)
	}
	if first["md5"] != hexMD5([]byte("01234")) {
		t.Fatalf("首片 md5 应为第一片内容: %v", first["md5"])
	}
	if m.finishBodies[1]["part_index"] != float64(1) {
		t.Fatalf("第二片 index 应为 1: %+v", m.finishBodies[1])
	}
	if m.finishBodies[2]["md5"] != hexMD5([]byte("ab")) {
		t.Fatalf("末片应为剩余 2 字节内容: %v", m.finishBodies[2]["md5"])
	}
	if string(m.parts["/put/c"]) != "ab" {
		t.Fatalf("末片内容应为剩余字节: %q", m.parts["/put/c"])
	}

	// ④ files:file_type + upload_id
	if m.filesPath != "/v2/users/openid-1/files" {
		t.Fatalf("files 路径异常: %s", m.filesPath)
	}
	if m.filesBody["file_type"] != float64(FileTypeFile) || m.filesBody["upload_id"] != "upload-1" {
		t.Fatalf("files 字段异常: %+v", m.filesBody)
	}

	// ⑤ 发送富媒体消息:msg_type=7 + media.file_info + 被动 msg_id/seq
	if err := c.SendMediaMessage(context.Background(), false, "openid-1", out.FileInfo, "qqmsg-1", 7); err != nil {
		t.Fatalf("发送富媒体应成功: %v", err)
	}
	sb := m.sendBody
	if sb["msg_type"] != float64(MsgTypeMedia) || sb["msg_id"] != "qqmsg-1" || sb["msg_seq"] != float64(7) {
		t.Fatalf("富媒体消息字段异常: %+v", sb)
	}
	media, _ := sb["media"].(map[string]any)
	if media["file_info"] != "fi-1" {
		t.Fatalf("media.file_info 异常: %+v", sb["media"])
	}
}

func TestUploadGroupEndpointDispatch(t *testing.T) {
	m, hs, ts := newMockUpload(t)
	c := NewClient(ts).WithBaseURL(hs.URL)
	m.partCount = 1
	if _, err := c.UploadLocalFile(context.Background(), true, "grp-1", FileTypeImage, "a.png", []byte("png")); err != nil {
		t.Fatalf("群上传应成功: %v", err)
	}
	// 单聊与群端点严格分派(不可互通)
	if m.preparePath != "/v2/groups/grp-1/upload_prepare" {
		t.Fatalf("群 prepare 路径异常: %s", m.preparePath)
	}
	if m.filesPath != "/v2/groups/grp-1/files" {
		t.Fatalf("群 files 路径异常: %s", m.filesPath)
	}
	if len(m.finishBodies) != 1 {
		t.Fatalf("应确认 1 片: %d", len(m.finishBodies))
	}
}

func TestUploadGuardsAndErrors(t *testing.T) {
	m, hs, ts := newMockUpload(t)
	c := NewClient(ts).WithBaseURL(hs.URL)
	ctx := context.Background()

	// 空内容 / 超硬限 → 本地拒绝(不发请求)
	if _, err := c.UploadLocalFile(ctx, false, "u", FileTypeFile, "a", nil); err == nil {
		t.Fatal("空内容应拒绝")
	}
	big := make([]byte, MediaHardMax+1)
	if _, err := c.UploadLocalFile(ctx, false, "u", FileTypeFile, "a", big); err == nil ||
		!strings.Contains(err.Error(), "硬限") {
		t.Fatalf("超硬限应拒绝: %v", err)
	}
	if m.prepareCount != 0 {
		t.Fatalf("本地拒绝不应发请求: %d", m.prepareCount)
	}
	// 空目标
	if _, err := c.UploadPrepare(ctx, false, "", UploadRequest{}); err == nil {
		t.Fatal("空目标应拒绝")
	}
	// 业务错误:尺寸超限 / 非法 file_info → 分类判定
	m.prepareErr = &APIError{Code: CodeMediaSizeOver, Message: "size"}
	if _, err := c.UploadLocalFile(ctx, false, "u", FileTypeFile, "a", []byte("x")); !IsMediaSizeOver(err) {
		t.Fatalf("应识别尺寸超限: %v", err)
	}
	m.prepareErr = nil
	m.filesErr = &APIError{Code: CodeInvalidFileInfo, Message: "bad file_info"}
	if _, err := c.UploadLocalFile(ctx, false, "u", FileTypeFile, "a", []byte("x")); !IsInvalidFileInfo(err) {
		t.Fatalf("应识别非法 file_info: %v", err)
	}
	m.filesErr = &APIError{Code: CodeDailyCapacity, Message: "capacity"}
	if _, err := c.UploadLocalFile(ctx, false, "u", FileTypeFile, "a", []byte("x")); !IsDailyCapacity(err) {
		t.Fatalf("应识别日容量超限: %v", err)
	}
	m.filesErr = nil
	// 分片 PUT 失败 → 显式错误(带 http 状态)
	m.putStatus = http.StatusInternalServerError
	if _, err := c.UploadLocalFile(ctx, false, "u", FileTypeFile, "a", []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "分片上传 http 500") {
		t.Fatalf("PUT 失败应显式报错: %v", err)
	}
	m.putStatus = 0
	// 可重试码判定
	if !IsUploadRetryable(&APIError{Code: CodeUploadRetryable}) {
		t.Fatal("应识别可重试码")
	}
	if APIErrorCode(&APIError{Code: CodeMediaTransfer}) != CodeMediaTransfer {
		t.Fatal("APIErrorCode 异常")
	}
	if APIErrorCode(context.Canceled) != 0 {
		t.Fatal("非业务错误应返回 0")
	}
	// 分片覆盖不足 → 显式拒绝(不静默上传截断文件)
	m3, hs3, ts3 := newMockUpload(t)
	m3.partCount = 2 // 12 字节却只给 2 片 × 5 字节
	c3 := NewClient(ts3).WithBaseURL(hs3.URL)
	if _, err := c3.UploadLocalFile(ctx, false, "u", FileTypeFile, "a", []byte("0123456789ab")); err == nil ||
		!strings.Contains(err.Error(), "分片覆盖不足") {
		t.Fatalf("覆盖不足应显式报错: %v", err)
	}
	// prepare 缺字段 → 结构化错误
	m2, hs2, ts2 := newMockUpload(t)
	m2.uploadID = ""
	c2 := NewClient(ts2).WithBaseURL(hs2.URL)
	if _, err := c2.UploadLocalFile(ctx, false, "u", FileTypeFile, "a", []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "缺 upload_id") {
		t.Fatalf("缺 upload_id 应报错: %v", err)
	}
}
