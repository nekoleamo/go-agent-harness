// 媒体出站(MED-3 / E5-1b,beta):iLink 三段式上传 —— ① getuploadurl 申请上传参数
// ② AES-128-ECB + PKCS7 加密后 POST CDN(取响应头 x-encrypted-param)③ sendmessage 媒体项。
//
// 契约来源:第三方逆向整理的协议规范(见 docs/IM_REMOTE.md §9.2;官方 SDK 未公开该链路的
// 稳定文档),字段名/版本差异需真机验证:
//   - v2.1+ 响应可能给 `upload_full_url`(替代 `upload_param` + 拼接 URL)→ 两者兼容;
//   - `aeskey` 出站统一 `base64(32 字符 hex 字符串)`;
//   - **无 context_token 不能投递**(微信无主动推送)→ 显式报错,不静默失败。
//
// 本文件只做协议编解码;媒体类型判定/产物登记由 im.MediaSender 实现方(ui-im-wechat)负责。
package ilink

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// DefaultCDNURL iLink 媒体 CDN 上传端点(§9.2 第 2 步);Client.CDNURL 可覆盖(测试/私有化)。
const DefaultCDNURL = "https://novac2c.cdn.weixin.qq.com/c2c/upload"

// media_type 取值(§9.2;与 media_type 请求字段对齐)。
const (
	MediaTypeImage = 1 // IMAGE
	MediaTypeVideo = 2 // VIDEO
	MediaTypeFile  = 3 // FILE
	MediaTypeVoice = 4 // VOICE
)

// 媒体项 item type(与 Item.Type 入站口径一致:1 文本/2 图片/3 语音/4 文件/5 视频)。
const (
	ItemTypeText  = 1
	ItemTypeImage = 2
	ItemTypeVoice = 3
	ItemTypeFile  = 4
	ItemTypeVideo = 5
)

// cdnRetryMax CDN 上传最大尝试次数(5xx 重试;4xx 立即中止)。
const cdnRetryMax = 3

// uploadHTTP 上传用 client(CDN 上传通常远快于消息接口;超时由 ctx 控制)。
var uploadHTTP = &http.Client{Timeout: 60 * time.Second}

// UploadRequest getuploadurl 请求体(§9.2 第 1 步;字段名严格按契约)。
type UploadRequest struct {
	FileKey    string // 随机 16 字节 hex(32 字符)
	MediaType  int    // MediaTypeImage/Video/File/Voice
	ToUserID   string // 接收方
	RawSize    int64  // 明文大小
	RawFileMD5 string // 明文 MD5(hex)
	FileSize   int64  // PKCS7 后密文大小(= PaddedSize(RawSize))
	AESKey     string // 16 字节 hex(32 字符)
}

// UploadTicket getuploadurl 响应(upload_param 与 upload_full_url 二者兼容)。
type UploadTicket struct {
	UploadParam      string `json:"upload_param"`
	UploadFullURL    string `json:"upload_full_url"`
	ThumbUploadParam string `json:"thumb_upload_param"`
}

// PaddedSize PKCS7 后密文大小 = ceil((rawsize+1)/16)*16(明文为 16 整数倍时也补满一块)。
func PaddedSize(raw int64) int64 {
	if raw < 0 {
		raw = 0
	}
	return (raw + aes.BlockSize) / aes.BlockSize * aes.BlockSize
}

// NewFileKey 随机 16 字节 hex(32 字符;协议要求的 filekey)。
func NewFileKey() (string, error) { return randomHex(16) }

// NewAESKey 随机 16 字节 hex(32 字符;协议要求的 aeskey)。
func NewAESKey() (string, error) { return randomHex(16) }

// randomHex 取 n 个随机字节的 hex 字符串。
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("ilink: 随机数生成失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// EncryptMedia AES-128-ECB + PKCS7 加密(出站第 2 步的 body;key 必须 16 字节)。
// 与 DecryptMedia 互逆(同一套 ECB/PKCS7 口径),便于回环自测。
func EncryptMedia(data, key []byte) ([]byte, error) {
	if len(key) != aes.BlockSize {
		return nil, fmt.Errorf("ilink: 加密密钥须 16 字节(实际 %d)", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	// PKCS7 填充(总是补 1..16 字节)
	pad := aes.BlockSize - len(data)%aes.BlockSize
	buf := make([]byte, len(data)+pad)
	copy(buf, data)
	for i := len(data); i < len(buf); i++ {
		buf[i] = byte(pad)
	}
	out := make([]byte, len(buf))
	for i := 0; i < len(buf); i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], buf[i:i+aes.BlockSize])
	}
	return out, nil
}

// MediaKeyB64 出站 aes_key 编码:base64(32 字符 hex 字符串)(§9.2)。
func MediaKeyB64(keyHex string) string {
	return base64.StdEncoding.EncodeToString([]byte(keyHex))
}

// GetUploadURL 三段式第 1 步:申请上传参数。
func (c *Client) GetUploadURL(ctx context.Context, req UploadRequest) (*UploadTicket, error) {
	raw, err := c.post(ctx, "ilink/bot/getuploadurl", map[string]any{
		"filekey":       req.FileKey,
		"media_type":    req.MediaType,
		"to_user_id":    req.ToUserID,
		"rawsize":       req.RawSize,
		"rawfilemd5":    req.RawFileMD5,
		"filesize":      req.FileSize,
		"aeskey":        req.AESKey,
		"no_need_thumb": true, // 官方现状不传缩略图
		"base_info":     baseInfo(),
	}, 15*time.Second)
	if err != nil {
		return nil, err
	}
	var out UploadTicket
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("ilink: getuploadurl 解码失败: %w", err)
	}
	if out.UploadParam == "" && out.UploadFullURL == "" {
		return nil, fmt.Errorf("ilink: getuploadurl 未返回上传参数(upload_param/upload_full_url 均为空)")
	}
	return &out, nil
}

// uploadURL 组装第 2 步的 CDN URL:优先 upload_full_url;否则 cdn 端点 + 查询参数。
func (c *Client) uploadURL(t *UploadTicket, fileKey string) string {
	if t.UploadFullURL != "" {
		return t.UploadFullURL
	}
	base := c.CDNURL
	if base == "" {
		base = DefaultCDNURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	q.Set("encrypted_query_param", t.UploadParam)
	q.Set("filekey", fileKey)
	u.RawQuery = q.Encode()
	return u.String()
}

// UploadToCDN 三段式第 2 步:POST 密文到 CDN,返回响应头 x-encrypted-param
// (即后续 CDNMedia.encrypt_query_param)。5xx 重试 ≤cdnRetryMax;4xx 立即中止。
func (c *Client) UploadToCDN(ctx context.Context, ticket *UploadTicket, fileKey string, ciphertext []byte) (string, error) {
	if ticket == nil {
		return "", fmt.Errorf("ilink: 缺少上传参数(先调用 GetUploadURL)")
	}
	target := c.uploadURL(ticket, fileKey)
	var lastErr error
	for attempt := 1; attempt <= cdnRetryMax; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(ciphertext))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Content-Length", strconv.Itoa(len(ciphertext)))
		resp, err := uploadHTTP.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("ilink: CDN 上传失败: %w", err)
		} else {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			switch {
			case resp.StatusCode == http.StatusOK:
				param := resp.Header.Get("x-encrypted-param")
				if param == "" {
					return "", fmt.Errorf("ilink: CDN 上传成功但缺少 x-encrypted-param 响应头")
				}
				return param, nil
			case resp.StatusCode >= 400 && resp.StatusCode < 500:
				// 4xx:参数/凭证问题,重试无意义 → 立即中止
				return "", fmt.Errorf("ilink: CDN 上传 HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
			default:
				lastErr = fmt.Errorf("ilink: CDN 上传 HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
			}
		}
		if attempt < cdnRetryMax {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
	}
	return "", lastErr
}

// MediaItem 出站媒体项(§9.2 第 3 步 item_list 的单元素)。
// 只填与 Type 相关的字段;AESKeyHex 为 16 字节 hex,payload() 统一编码为 base64(hex)。
type MediaItem struct {
	Type              int    // ItemTypeImage/Voice/File/Video
	EncryptQueryParam string // CDN 上传返回的 x-encrypted-param
	AESKeyHex         string // 16 字节 hex(32 字符)
	EncryptType       int    // 出站固定 1(打包附加信息)
	Size              int64  // 密文大小(image_item.mid_size / video_item.video_size)
	FileName          string // 文件项显示名
	MD5               string // 文件项明文 MD5(hex)
	RawSize           int64  // 文件项 len(明文字符串)
	PlayLength        int    // 视频项时长(ms)
}

// payload 媒体项 JSON 形状(字段按 Type 组合;非媒体类型返回错误防静默错发)。
func (m MediaItem) payload() (map[string]any, error) {
	if m.EncryptQueryParam == "" {
		return nil, fmt.Errorf("ilink: 媒体项缺少 encrypt_query_param")
	}
	encType := m.EncryptType
	media := map[string]any{
		"encrypt_query_param": m.EncryptQueryParam,
		"aes_key":             MediaKeyB64(m.AESKeyHex),
		"encrypt_type":        encType,
	}
	switch m.Type {
	case ItemTypeImage:
		return map[string]any{"type": ItemTypeImage, "image_item": map[string]any{
			"media": media, "mid_size": m.Size,
		}}, nil
	case ItemTypeVoice:
		return map[string]any{"type": ItemTypeVoice, "voice_item": map[string]any{
			"media": media,
		}}, nil
	case ItemTypeFile:
		return map[string]any{"type": ItemTypeFile, "file_item": map[string]any{
			"media": media, "file_name": m.FileName, "md5": m.MD5, "len": strconv.FormatInt(m.RawSize, 10),
		}}, nil
	case ItemTypeVideo:
		return map[string]any{"type": ItemTypeVideo, "video_item": map[string]any{
			"media": media, "video_size": m.Size, "play_length": m.PlayLength,
		}}, nil
	}
	return nil, fmt.Errorf("ilink: 不支持的媒体项类型 %d(仅 2 图片/3 语音/4 文件/5 视频)", m.Type)
}

// SendMediaMessage 三段式第 3 步:发送媒体项消息。
// context_token 必需(微信无主动推送);缺失显式报错。
func (c *Client) SendMediaMessage(ctx context.Context, to, contextToken, clientID string, item MediaItem) error {
	if c.Token == "" {
		return ErrNotLoggedIn
	}
	if contextToken == "" {
		return fmt.Errorf("ilink: 缺少 context_token,无法投递媒体(需对方先发消息)")
	}
	it, err := item.payload()
	if err != nil {
		return err
	}
	if clientID == "" {
		clientID = "gah-ilink-" + fmt.Sprint(time.Now().UnixNano())
	}
	_, err = c.post(ctx, "ilink/bot/sendmessage", map[string]any{
		"msg": map[string]any{
			"from_user_id":  "",
			"to_user_id":    to,
			"client_id":     clientID,
			"message_type":  2, // BOT
			"message_state": 2, // FINISH
			"item_list":     []map[string]any{it},
			"context_token": contextToken,
		},
		"base_info": baseInfo(),
	}, 30*time.Second)
	return err
}
