// 出站媒体(MED-2):qqTransport 实现 im.MediaSender —— 分片上传本地产物 + msg_type=7 富媒体消息。
//
// 投递口径(与文本一致,不新增旁路):
//   - 被动窗口内(缓存 msg_id,5min)→ 带 msg_id + 自增 msg_seq,不耗主动配额;
//   - 无被动上下文 → 主动配额记账(私信 2 条/天/用户);配额耗尽/频控 → **显式报错**
//     (媒体条目不入 ledger;桥会回滚登记条目,模型可重试 —— 可见优于静默滞留);
//   - `file_info` 有 TTL:按 (chat|路径|size|mtime|file_type) 缓存复用,`304080`/过期 → 重传一次。
package uimqq

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/im"
	"github.com/nekoleamo/go-agent-harness/qqbot"
)

// qqMediaAPI SendMedia 所需的最小客户端面(便于单测注入替身;*qqbot.Client 天然满足)。
type qqMediaAPI interface {
	UploadLocalFile(ctx context.Context, isGroup bool, target string, fileType int, name string, data []byte) (qqbot.UploadedFile, error)
	SendC2CMessage(ctx context.Context, openid string, msg qqbot.SendMessage) error
	SendGroupMessage(ctx context.Context, groupOpenID string, msg qqbot.SendMessage) error
}

// mediaCacheCap file_info 缓存条数上限(超出淘汰最旧)。
const mediaCacheCap = 16

// mediaInfoEntry 一次上传的 file_info 缓存(TTL 内复用,避免重复上传)。
type mediaInfoEntry struct {
	info      string
	expiresAt time.Time
	at        time.Time
}

// fileTypeForKind 媒体类型 → 官方 file_type(MED-2):
// 图片仅 png/jpg 走 image,视频仅 mp4,语音仅 silk;其余或超软限一律降级为「文件」。
func fileTypeForKind(kind im.MediaKind, name string, size int64) (int, string) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	switch kind {
	case im.MediaImage:
		if (ext == "png" || ext == "jpg" || ext == "jpeg") && size <= qqbot.MediaSoftImageMax {
			return qqbot.FileTypeImage, ""
		}
		return qqbot.FileTypeFile, "图片超软限或格式非 png/jpg,已按文件类型上传"
	case im.MediaVideo:
		if ext == "mp4" && size <= qqbot.MediaSoftVideoMax {
			return qqbot.FileTypeVideo, ""
		}
		return qqbot.FileTypeFile, "视频超软限或非 mp4,已按文件类型上传"
	case im.MediaVoice:
		if ext == "silk" && size <= qqbot.MediaSoftVoiceMax {
			return qqbot.FileTypeVoice, ""
		}
		return qqbot.FileTypeFile, "语音超软限或非 silk,已按文件类型上传"
	}
	return qqbot.FileTypeFile, ""
}

// mediaClient 取当前客户端(未配置/网关未启 → 显式错误)。
// mediaAPIOverride 仅测试注入(替身);生产为 *qqbot.Client。
func (t *qqTransport) mediaClient() (qqMediaAPI, error) {
	t.mu.Lock()
	cli, override := t.client, t.mediaAPIOverride
	t.mu.Unlock()
	if override != nil {
		return override, nil
	}
	if cli == nil {
		return nil, fmt.Errorf("qq: 网关未运行(未配置 /qq login?)")
	}
	return cli, nil
}

// SendMedia 实现 im.MediaSender(MED-2)。
func (t *qqTransport) SendMedia(ctx context.Context, to im.Route, m im.MediaPayload) error {
	if m.ArtifactID == "" || m.Path == "" {
		return errors.New("qq: 出站媒体必须是已登记产物")
	}
	cli, err := t.mediaClient()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(m.Path)
	if err != nil {
		return fmt.Errorf("qq: 产物不可读: %w", err)
	}
	if m.Size > 0 && int64(len(data)) != m.Size {
		return fmt.Errorf("qq: 产物大小与登记不符(%d != %d)", len(data), m.Size)
	}
	fileType, downgrade := fileTypeForKind(m.Kind, m.Name, int64(len(data)))
	isGroup := to.Group || to.ChatID != to.UserID

	info, err := t.mediaFileInfo(ctx, cli, isGroup, to.ChatID, m, fileType, data)
	if err != nil {
		return err
	}
	if err := t.sendMediaMessage(ctx, cli, to, isGroup, info, m.Name); err != nil {
		// file_info 过期/非法 → 清缓存重传一次
		if qqbot.IsInvalidFileInfo(err) {
			t.dropMediaInfo(mediaCacheKey(to.ChatID, m, fileType))
			info, uerr := t.mediaFileInfo(ctx, cli, isGroup, to.ChatID, m, fileType, data)
			if uerr != nil {
				return uerr
			}
			if rerr := t.sendMediaMessage(ctx, cli, to, isGroup, info, m.Name); rerr != nil {
				return rerr
			}
		} else {
			return err
		}
	}
	if downgrade != "" {
		t.setLastError("出站媒体提示: " + downgrade)
	}
	return nil
}

// mediaFileInfo 取(或上传并缓存)file_info。
func (t *qqTransport) mediaFileInfo(ctx context.Context, cli qqMediaAPI, isGroup bool, chatID string,
	m im.MediaPayload, fileType int, data []byte) (string, error) {
	key := mediaCacheKey(chatID, m, fileType)
	t.mu.Lock()
	entry, ok := t.mediaInfos[key]
	t.mu.Unlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.info, nil
	}
	up, err := cli.UploadLocalFile(ctx, isGroup, chatID, fileType, m.Name, data)
	if err != nil {
		t.setLastError("出站媒体上传失败: " + err.Error())
		return "", err
	}
	ttl := time.Duration(up.ExpiresIn())
	if ttl <= 0 {
		ttl = time.Hour // 官方未给 TTL 时的保守默认
	}
	t.mu.Lock()
	if t.mediaInfos == nil {
		t.mediaInfos = map[string]mediaInfoEntry{}
	}
	if len(t.mediaInfos) >= mediaCacheCap {
		var oldestKey string
		var oldest time.Time
		for k, v := range t.mediaInfos {
			if oldestKey == "" || v.at.Before(oldest) {
				oldestKey, oldest = k, v.at
			}
		}
		delete(t.mediaInfos, oldestKey)
	}
	t.mediaInfos[key] = mediaInfoEntry{info: up.FileInfo, expiresAt: time.Now().Add(ttl), at: time.Now()}
	t.mu.Unlock()
	return up.FileInfo, nil
}

// dropMediaInfo 失效缓存(重传前调用)。
func (t *qqTransport) dropMediaInfo(key string) {
	t.mu.Lock()
	delete(t.mediaInfos, key)
	t.mu.Unlock()
}

// mediaCacheKey file_info 缓存键(chat + 路径 + size + mtime + file_type)。
func mediaCacheKey(chatID string, m im.MediaPayload, fileType int) string {
	return strings.Join([]string{
		chatID, m.Path, strconv.FormatInt(m.Size, 10), strconv.FormatInt(m.TimeUnixNano, 10), strconv.Itoa(fileType),
	}, "|")
}

// sendMediaMessage 发送富媒体消息(被动优先;无被动上下文走主动配额,配额/频控 → 显式错误)。
func (t *qqTransport) sendMediaMessage(ctx context.Context, cli qqMediaAPI, to im.Route, isGroup bool,
	fileInfo, name string) error {
	t.mu.Lock()
	msgID := ""
	if rc, ok := t.replies[to.ChatID]; ok && time.Since(rc.receivedAt) <= replyWindow {
		msgID = rc.msgID
	}
	baseSeq := t.seq[to.ChatID]
	t.mu.Unlock()

	send := func(msgID string, seq uint64) error {
		msg := qqbot.SendMessage{MsgType: qqbot.MsgTypeMedia, Media: &qqbot.MediaInfo{FileInfo: fileInfo}, MsgID: msgID, MsgSeq: seq}
		if isGroup {
			return cli.SendGroupMessage(ctx, to.ChatID, msg)
		}
		return cli.SendC2CMessage(ctx, to.ChatID, msg)
	}
	if msgID != "" {
		if err := send(msgID, baseSeq+1); err != nil {
			if qqbot.IsRateLimited(err) {
				t.setLastError("出站媒体频控: " + err.Error())
			}
			return fmt.Errorf("qq: 富媒体发送失败(%s): %w", name, err)
		}
		t.mu.Lock()
		t.seq[to.ChatID] = baseSeq + 1
		t.mu.Unlock()
		return nil
	}
	// 无被动上下文:主动配额一次(配额耗尽/频控 → 显式报错,不静默滞留)
	if t.budget != nil && !t.budget.Allow(to.SenderKey()) {
		return fmt.Errorf("qq: 主动消息配额已用尽,无法主动投递文件 %s(请先与机器人对话以开启被动窗口)", name)
	}
	if err := send("", 0); err != nil {
		if qqbot.IsRateLimited(err) {
			t.setLastError("出站媒体主动频控: " + err.Error())
		}
		return fmt.Errorf("qq: 富媒体主动投递失败(%s): %w", name, err)
	}
	if t.budget != nil {
		_ = t.budget.Consume(to.SenderKey())
	}
	return nil
}
