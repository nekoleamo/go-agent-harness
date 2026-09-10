// 富媒体分片上传(MED-2;契约复核见 docs/IM_REMOTE §9.1)。
//
// 四步(官方):
//  1. `POST /v2/{users|groups}/{id}/upload_prepare`
//     入:`file_type`(1 图片/2 视频/3 语音/4 文件)、`file_size`(**字符串**)、`file_name`、`md5`、`sha1`、`md5_10m`
//     出:`upload_id`、`block_size`(字符串)、`parts[]{index,presigned_url,block_size}`、`upload_config`
//  2. 分片 `PUT <presigned_url>`(**无鉴权头**,裸字节)
//  3. `POST /v2/{users|groups}/{id}/upload_part_finish`:`{upload_id, part_index, block_size, md5}`(10 QPS;响应 {})
//  4. `POST /v2/{users|groups}/{id}/files`:`{file_type, upload_id}` → `{file_uuid, file_info, ttl}`
//
// 关键纪律:
//   - **单聊与群上传端点不互通**(文件不可跨场景复用);
//   - `index` 一律**以 prepare 响应为准**回传(官方示例从 0 起,社区有按 1 起的实现 → 不自行假设);
//   - 分片按响应顺序串行上传(并发/重试参数来自 `upload_config`,当前保守串行,避免 50 QPS 群限流);
//   - 失败带错误码分类(850031 尺寸 / 850019 格式 / 40093002 日容量 / 40093001 可重试)。
package qqbot

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// 富媒体上传相关错误码(官方文档)。
const (
	CodeMediaSizeOver    = 850031 // 文件超过限制
	CodeMediaBadFormat   = 850019 // 不支持的格式
	CodeMediaDownloadErr = 850026 // 拉取文件失败(URL 上传)
	CodeInvalidFileInfo  = 304080 // file_info 非法/过期
	CodeMediaTransfer    = 40034004
	CodeUploadRetryable  = 40093001 // 分片上传可重试
	CodeDailyCapacity    = 40093002 // 当日文件容量超限
)

// 富媒体类型(与 SendMessage.MsgType 区分:这里是上传 file_type)。
const (
	FileTypeImage = 1
	FileTypeVideo = 2
	FileTypeVoice = 3
	FileTypeFile  = 4
)

// 上传大小护栏(MED-2:官方软/硬限制子集;超软限降级为文件类型,超硬限拒绝)。
const (
	MediaSoftImageMax = 20 << 20 // 图片软限 20MB
	MediaSoftVideoMax = 30 << 20 // 视频软限 30MB
	MediaSoftVoiceMax = 20 << 20 // 语音软限 20MB
	MediaHardMax      = 200 << 20
	md5_10mSize       = 10002432 // 官方字段名 md5_10m:前 10002432 字节的 MD5
)

// UploadRequest 预上传请求(字段名与官方一致;file_size 为字符串)。
type UploadRequest struct {
	FileType int    `json:"file_type"`
	FileSize string `json:"file_size"`
	FileName string `json:"file_name"`
	MD5      string `json:"md5"`
	SHA1     string `json:"sha1"`
	MD5_10m  string `json:"md5_10m"`
}

// UploadPart 一个分片(index 以响应为准)。
type UploadPart struct {
	Index        int    `json:"index"`
	PresignedURL string `json:"presigned_url"`
	BlockSize    string `json:"block_size"`
}

// UploadConfig 服务端下发上传参数(并发/重试)。
type UploadConfig struct {
	Concurrency  int `json:"concurrency"`
	RetryTimeout int `json:"retry_timeout"`
	RetryDelay   int `json:"retry_delay"`
}

// UploadPrepare 预上传响应。
type UploadPrepare struct {
	UploadID     string       `json:"upload_id"`
	BlockSize    string       `json:"block_size"`
	Parts        []UploadPart `json:"parts"`
	UploadConfig UploadConfig `json:"upload_config"`
}

// UploadedFile 上传完成结果。
type UploadedFile struct {
	FileUUID string `json:"file_uuid"`
	FileInfo string `json:"file_info"`
	TTL      int64  `json:"ttl"` // 官方 TTL(单位以真机为准;>1e9 视为毫秒)
	Bytes    int64  `json:"-"`
}

// ExpiresIn TTL 归一为时长(file_info 过期后需重新上传)。
func (u UploadedFile) ExpiresIn() (d timeDuration) {
	switch {
	case u.TTL <= 0:
		return 0
	case u.TTL > 1e9: // 毫秒
		return timeDuration(u.TTL) * timeDuration(1e6)
	default: // 秒
		return timeDuration(u.TTL) * timeDuration(1e9)
	}
}

// timeDuration 本地别名(避免与 time 包重名歧义;实际即纳秒计数)。
type timeDuration = int64

// uploadPath 组装上传端点路径(单聊/群严格分派)。
func uploadPath(isGroup bool, target, suffix string) string {
	if isGroup {
		return "/v2/groups/" + target + suffix
	}
	return "/v2/users/" + target + suffix
}

// UploadPrepare 预上传(申请 upload_id 与分片预签名 URL)。
func (c *Client) UploadPrepare(ctx context.Context, isGroup bool, target string, req UploadRequest) (*UploadPrepare, error) {
	if strings.TrimSpace(target) == "" {
		return nil, errors.New("qqbot: 上传目标为空")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := c.postRaw(ctx, uploadPath(isGroup, target, "/upload_prepare"), payload, true)
	if err != nil {
		return nil, err
	}
	var out UploadPrepare
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("qqbot: upload_prepare 响应解析失败: %w", err)
	}
	if out.UploadID == "" {
		return nil, fmt.Errorf("qqbot: upload_prepare 缺 upload_id: %s", truncate(string(raw), 200))
	}
	return &out, nil
}

// PutPart 上传单个分片(**预签名 URL 自带凭据:不带 QQBot 鉴权头**)。
func (c *Client) PutPart(ctx context.Context, presignedURL string, data []byte) error {
	if presignedURL == "" {
		return errors.New("qqbot: 分片预签名 URL 为空")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("qqbot: 分片上传失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("qqbot: 分片上传 http %d: %s", resp.StatusCode, truncate(string(raw), 160))
	}
	return nil
}

// UploadPartFinish 确认单个分片(10 QPS;part_index 用 prepare 返回的 index)。
func (c *Client) UploadPartFinish(ctx context.Context, isGroup bool, target, uploadID string, partIndex int, blockSize, md5Hex string) error {
	body, err := json.Marshal(map[string]any{
		"upload_id":  uploadID,
		"part_index": partIndex,
		"block_size": blockSize,
		"md5":        md5Hex,
	})
	if err != nil {
		return err
	}
	_, err = c.postRaw(ctx, uploadPath(isGroup, target, "/upload_part_finish"), body, true)
	return err
}

// CompleteUpload 合并分片拿 file_info(POST files {file_type, upload_id})。
func (c *Client) CompleteUpload(ctx context.Context, isGroup bool, target string, fileType int, uploadID string) (UploadedFile, error) {
	body, err := json.Marshal(map[string]any{"file_type": fileType, "upload_id": uploadID})
	if err != nil {
		return UploadedFile{}, err
	}
	raw, err := c.postRaw(ctx, uploadPath(isGroup, target, "/files"), body, true)
	if err != nil {
		return UploadedFile{}, err
	}
	var out UploadedFile
	if err := json.Unmarshal(raw, &out); err != nil {
		return UploadedFile{}, fmt.Errorf("qqbot: files 响应解析失败: %w", err)
	}
	if out.FileInfo == "" {
		return UploadedFile{}, fmt.Errorf("qqbot: files 响应缺 file_info: %s", truncate(string(raw), 200))
	}
	return out, nil
}

// UploadLocalFile 本地文件四步上传(MED-2 主入口)。
// fileType 由调用方按 Kind 决定(超软限时可自行降级为 FileTypeFile)。
func (c *Client) UploadLocalFile(ctx context.Context, isGroup bool, target string, fileType int, name string, data []byte) (UploadedFile, error) {
	if len(data) == 0 {
		return UploadedFile{}, errors.New("qqbot: 待上传内容为空")
	}
	if len(data) > MediaHardMax {
		return UploadedFile{}, fmt.Errorf("qqbot: 文件超硬限(%d > %d)", len(data), int64(MediaHardMax))
	}
	prep, err := c.UploadPrepare(ctx, isGroup, target, UploadRequest{
		FileType: fileType,
		FileSize: strconv.Itoa(len(data)),
		FileName: name,
		MD5:      hexMD5(data),
		SHA1:     hexSHA1(data),
		MD5_10m:  hexMD5(head(data, md5_10mSize)),
	})
	if err != nil {
		return UploadedFile{}, err
	}
	if len(prep.Parts) == 0 {
		return UploadedFile{}, errors.New("qqbot: upload_prepare 未返回分片")
	}
	// 按 prepare 返回顺序切分并逐片上传(串行:群上传 50 QPS/part 10 QPS 保守策略)
	offset := 0
	for _, p := range prep.Parts {
		n := len(data) - offset
		if p.BlockSize != "" {
			if bs, err := strconv.Atoi(p.BlockSize); err == nil && bs > 0 && bs < n {
				n = bs
			}
		}
		if n < 0 {
			n = 0
		}
		chunk := data[offset : offset+n]
		if err := c.PutPart(ctx, p.PresignedURL, chunk); err != nil {
			return UploadedFile{}, err
		}
		if err := c.UploadPartFinish(ctx, isGroup, target, prep.UploadID, p.Index, p.BlockSize, hexMD5(chunk)); err != nil {
			return UploadedFile{}, err
		}
		offset += n
	}
	// 护栏:prepare 返回的分片必须覆盖全部字节,否则拒绝(绝不静默上传截断文件)
	if offset != len(data) {
		return UploadedFile{}, fmt.Errorf("qqbot: 分片覆盖不足(%d/%d 字节;upload_prepare 返回 %d 片)",
			offset, len(data), len(prep.Parts))
	}
	out, err := c.CompleteUpload(ctx, isGroup, target, fileType, prep.UploadID)
	if err != nil {
		return UploadedFile{}, err
	}
	out.Bytes = int64(len(data))
	return out, nil
}

// SendMediaMessage 发送富媒体消息(msg_type=7 + media.file_info;被动窗口内应带 msgID/seq)。
func (c *Client) SendMediaMessage(ctx context.Context, isGroup bool, target, fileInfo, msgID string, seq uint64) error {
	msg := SendMessage{MsgType: MsgTypeMedia, Media: &MediaInfo{FileInfo: fileInfo}, MsgID: msgID, MsgSeq: seq}
	if isGroup {
		return c.SendGroupMessage(ctx, target, msg)
	}
	return c.SendC2CMessage(ctx, target, msg)
}

// postRaw 带鉴权的 POST 并返回响应体(复用 do 的鉴权/业务错误/401 重试语义)。
func (c *Client) postRaw(ctx context.Context, path string, body []byte, allowRetry bool) ([]byte, error) {
	token, err := c.TS.Token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "QQBot "+token)
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && allowRetry {
		c.TS.Invalidate()
		return c.postRaw(ctx, path, body, false)
	}
	var biz struct {
		Code    flexInt `json:"code"`
		Message string  `json:"message"`
	}
	if err := json.Unmarshal(raw, &biz); err == nil && biz.Code != 0 {
		return nil, &APIError{Code: int(biz.Code), Message: biz.Message}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &APIError{Code: CodeRateLimited, Message: "http 429"}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qqbot: %s http %d: %s", path, resp.StatusCode, truncate(string(raw), 200))
	}
	return raw, nil
}

// —— 校验值工具 ——

func hexMD5(b []byte) string  { s := md5.Sum(b); return hex.EncodeToString(s[:]) }
func hexSHA1(b []byte) string { s := sha1.Sum(b); return hex.EncodeToString(s[:]) }

func head(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}

// IsInvalidFileInfo file_info 非法/过期(需重新上传一次)。
func IsInvalidFileInfo(err error) bool { return apiCodeIs(err, CodeInvalidFileInfo) }

// IsMediaSizeOver 文件超限。
func IsMediaSizeOver(err error) bool { return apiCodeIs(err, CodeMediaSizeOver) }

// IsDailyCapacity 当日文件容量超限。
func IsDailyCapacity(err error) bool { return apiCodeIs(err, CodeDailyCapacity) }

// IsUploadRetryable 分片上传可重试错误。
func IsUploadRetryable(err error) bool { return apiCodeIs(err, CodeUploadRetryable) }

// apiCodeIs 判定错误是否携带指定业务码。
// APIErrorCode 业务码(0 = 非业务错误)。
func APIErrorCode(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return 0
}

func apiCodeIs(err error, code int) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code == code
	}
	return false
}
