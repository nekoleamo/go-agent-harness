// 出站媒体(MED-3 / E5-1b,**beta**):wechatTransport 实现 im.MediaSender —— iLink 三段式
// (getuploadurl → AES-128-ECB 加密上传 CDN → 媒体项 sendmessage)。
//
// 投递口径(与文本一致,不新增旁路):
//   - 产物必须是己方登记条目(桥的产物账本);通道侧复核大小与登记一致;
//   - **无 context_token 不能投递**(微信无主动推送)→ 显式报错(桥回滚登记条目,模型可重试);
//   - 群投递不支持(iLink 为单聊会话);加密与密文全内存(≤ 平台限额,不落盘)。
//
// 证据强度:契约来自第三方逆向整理(docs/IM_REMOTE.md §9.2),字段名/版本差异需真机验证,
// 故本能力**标记 beta**:真机核对 `x-encrypted-param` 与媒体项字段后转正。失败一律显式返回。
package uimwechat

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nekoleamo/go-agent-harness/ilink"
	"github.com/nekoleamo/go-agent-harness/im"
)

// maxOutMediaBytes 出站媒体大小上限(与产物登记默认 media_max_mb=20 对齐;超限显式报错)。
const maxOutMediaBytes = 20 << 20

// wechatMediaAPI SendMedia 所需的最小客户端面(单测注入替身;*ilink.Client 天然满足)。
type wechatMediaAPI interface {
	GetUploadURL(ctx context.Context, req ilink.UploadRequest) (*ilink.UploadTicket, error)
	UploadToCDN(ctx context.Context, ticket *ilink.UploadTicket, fileKey string, ciphertext []byte) (string, error)
	SendMediaMessage(ctx context.Context, to, contextToken, clientID string, item ilink.MediaItem) error
}

// mediaTypeForKind 登记类型 → (iLink media_type, 媒体项 item type)。
// 不完全匹配平台枚举的一律**降级为文件**(与 QQ 侧口径一致):图片仅 png/jpg/gif/webp/bmp,
// 视频仅 mp4;语音无稳定发送 helper(§9.2)→ 文件。降级说明回填 lastError 使行为可见。
func mediaTypeForKind(kind im.MediaKind, name string) (int, int, string) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	switch kind {
	case im.MediaImage:
		switch ext {
		case "png", "jpg", "jpeg", "gif", "webp", "bmp":
			return ilink.MediaTypeImage, ilink.ItemTypeImage, ""
		}
		return ilink.MediaTypeFile, ilink.ItemTypeFile, "图片格式非 png/jpg/gif/webp/bmp,已按文件类型发送"
	case im.MediaVideo:
		if ext == "mp4" {
			return ilink.MediaTypeVideo, ilink.ItemTypeVideo, ""
		}
		return ilink.MediaTypeFile, ilink.ItemTypeFile, "视频非 mp4,已按文件类型发送"
	case im.MediaVoice:
		return ilink.MediaTypeFile, ilink.ItemTypeFile, "语音无稳定发送端点,已按文件类型发送"
	}
	return ilink.MediaTypeFile, ilink.ItemTypeFile, ""
}

// mediaClient 取当前媒体客户端(测试替身优先;未登录 → 显式错误)。
func (t *wechatTransport) mediaClient() (wechatMediaAPI, error) {
	t.mu.Lock()
	cli, override := t.client, t.mediaAPIOverride
	t.mu.Unlock()
	if override != nil {
		return override, nil
	}
	if cli == nil {
		return nil, errors.New("wechat: 未登录(执行 /wechat login 扫码)")
	}
	return cli, nil
}

// SendMedia 实现 im.MediaSender(MED-3,beta)。
func (t *wechatTransport) SendMedia(ctx context.Context, to im.Route, m im.MediaPayload) error {
	if to.Group {
		return errors.New("wechat: 该渠道不支持群投递(iLink 为单聊会话)")
	}
	if m.ArtifactID == "" || m.Path == "" {
		return errors.New("wechat: 出站媒体必须是已登记产物")
	}
	api, err := t.mediaClient()
	if err != nil {
		return err
	}
	t.mu.Lock()
	token := t.tokens[to.UserID]
	t.mu.Unlock()
	if token == "" {
		return fmt.Errorf("wechat: 无 %s 的 context_token,无法投递媒体(请对方先发消息)", to.UserID)
	}
	data, err := os.ReadFile(m.Path)
	if err != nil {
		return fmt.Errorf("wechat: 产物不可读: %w", err)
	}
	if m.Size > 0 && int64(len(data)) != m.Size {
		return fmt.Errorf("wechat: 产物大小与登记不符(%d != %d)", len(data), m.Size)
	}
	if int64(len(data)) > maxOutMediaBytes {
		return fmt.Errorf("wechat: 出站媒体超 %dMB 上限", maxOutMediaBytes>>20)
	}
	mediaType, itemType, downgrade := mediaTypeForKind(m.Kind, m.Name)

	keyHex, err := ilink.NewAESKey()
	if err != nil {
		return err
	}
	fileKey, err := ilink.NewFileKey()
	if err != nil {
		return err
	}
	rawKey, err := hex.DecodeString(keyHex)
	if err != nil {
		return fmt.Errorf("wechat: 密钥编码异常: %w", err)
	}
	cipher, err := ilink.EncryptMedia(data, rawKey)
	if err != nil {
		return err
	}
	sum := md5.Sum(data)
	md5Hex := hex.EncodeToString(sum[:])

	ticket, err := api.GetUploadURL(ctx, ilink.UploadRequest{
		FileKey: fileKey, MediaType: mediaType, ToUserID: to.UserID,
		RawSize: int64(len(data)), RawFileMD5: md5Hex,
		FileSize: int64(len(cipher)), AESKey: keyHex,
	})
	if err != nil {
		t.setLastError("出站媒体申请上传参数失败: " + err.Error())
		return fmt.Errorf("wechat: 媒体上传参数申请失败: %w", err)
	}
	param, err := api.UploadToCDN(ctx, ticket, fileKey, cipher)
	if err != nil {
		t.setLastError("出站媒体 CDN 上传失败: " + err.Error())
		return fmt.Errorf("wechat: 媒体 CDN 上传失败: %w", err)
	}
	item := ilink.MediaItem{
		Type: itemType, EncryptQueryParam: param, AESKeyHex: keyHex, EncryptType: 1,
		Size: int64(len(cipher)), FileName: m.Name, MD5: md5Hex, RawSize: int64(len(data)),
	}
	if err := api.SendMediaMessage(ctx, to.UserID, token, "", item); err != nil {
		if ilink.SessionExpired(err) {
			t.setLastError("微信会话已过期,媒体投递失败(需重新扫码登录)")
		}
		return fmt.Errorf("wechat: 媒体消息发送失败(%s): %w", m.Name, err)
	}
	if downgrade != "" {
		t.setLastError("出站媒体提示: " + downgrade)
	}
	return nil
}
