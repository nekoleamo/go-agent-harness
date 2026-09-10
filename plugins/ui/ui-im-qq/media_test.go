// 出站媒体(MED-2)单测:类型映射与软限降级 / 被动优先 / 主动配额 / file_info 缓存与失效重传 /
// 失败显式可见。用注入替身,零网络。
package uimqq

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/im"
	"github.com/nekoleamo/go-agent-harness/qqbot"
)

// fakeMediaAPI 记录上传与发送(可编程失败)。
type fakeMediaAPI struct {
	uploads   int
	lastType  int
	lastName  string
	lastBytes int
	sends     []qqbot.SendMessage
	sendPath  []string
	uploadErr error
	sendErr   error
	fileInfo  string
	ttl       int64
}

func (f *fakeMediaAPI) UploadLocalFile(_ context.Context, _ bool, _ string, fileType int, name string, data []byte) (qqbot.UploadedFile, error) {
	f.uploads++
	f.lastType, f.lastName, f.lastBytes = fileType, name, len(data)
	if f.uploadErr != nil {
		return qqbot.UploadedFile{}, f.uploadErr
	}
	info := f.fileInfo
	if info == "" {
		info = "fi-fake"
	}
	return qqbot.UploadedFile{FileUUID: "uuid", FileInfo: info, TTL: f.ttl, Bytes: int64(len(data))}, nil
}

func (f *fakeMediaAPI) SendC2CMessage(_ context.Context, openid string, msg qqbot.SendMessage) error {
	f.sends = append(f.sends, msg)
	f.sendPath = append(f.sendPath, "c2c:"+openid)
	if f.sendErr != nil {
		err := f.sendErr
		f.sendErr = nil // 只失败一次(便于验证重传路径)
		return err
	}
	return nil
}

func (f *fakeMediaAPI) SendGroupMessage(_ context.Context, gid string, msg qqbot.SendMessage) error {
	f.sends = append(f.sends, msg)
	f.sendPath = append(f.sendPath, "group:"+gid)
	return f.sendErr
}

// mediaTransport 待测 transport(fake 客户端 + 可选配额)。
func mediaTransport(t *testing.T, api qqMediaAPI, budget *activeQuota) (*qqTransport, im.MediaPayload) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "chart.png")
	if err := os.WriteFile(p, []byte("PNGDATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	tr := &qqTransport{
		name: channelName, client: nil, replies: map[string]*replyCtx{}, seq: map[string]uint64{},
		budget: budget, mediaInfos: map[string]mediaInfoEntry{}, remainder: map[string]string{},
	}
	// 注入替身(测试直连:client 字段为 *qqbot.Client;替身经 mediaClientOverride 暴露)
	tr.mediaAPIOverride = api
	m := im.MediaPayload{
		ArtifactID: "art-1", Kind: im.MediaImage, Path: p, Name: "chart.png",
		Mime: "image/png", Size: fi.Size(), TimeUnixNano: fi.ModTime().UnixNano(),
	}
	return tr, m
}

func TestFileTypeForKind(t *testing.T) {
	cases := []struct {
		kind im.MediaKind
		name string
		size int64
		want int
	}{
		{im.MediaImage, "a.png", 1024, qqbot.FileTypeImage},
		{im.MediaImage, "a.JPG", 1024, qqbot.FileTypeImage},
		{im.MediaImage, "a.gif", 1024, qqbot.FileTypeFile},                        // 非 png/jpg
		{im.MediaImage, "a.png", qqbot.MediaSoftImageMax + 1, qqbot.FileTypeFile}, // 超软限降级
		{im.MediaVideo, "a.mp4", 1024, qqbot.FileTypeVideo},
		{im.MediaVideo, "a.mov", 1024, qqbot.FileTypeFile},
		{im.MediaVideo, "a.mp4", qqbot.MediaSoftVideoMax + 1, qqbot.FileTypeFile},
		{im.MediaVoice, "a.silk", 1024, qqbot.FileTypeVoice},
		{im.MediaVoice, "a.mp3", 1024, qqbot.FileTypeFile},
		{im.MediaFile, "a.zip", 1024, qqbot.FileTypeFile},
	}
	for _, c := range cases {
		got, _ := fileTypeForKind(c.kind, c.name, c.size)
		if got != c.want {
			t.Fatalf("fileTypeForKind(%s,%s,%d)=%d want %d", c.kind, c.name, c.size, got, c.want)
		}
	}
	// 降级应有说明文案(供 lastError 诊断)
	if _, note := fileTypeForKind(im.MediaImage, "a.gif", 10); note == "" {
		t.Fatal("降级应给出说明")
	}
}

func TestSendMediaPassiveWindowAndCache(t *testing.T) {
	api := &fakeMediaAPI{ttl: 3600}
	tr, m := mediaTransport(t, api, nil)
	route := im.Route{Channel: channelName, UserID: "u1", ChatID: "u1"}
	// 无被动上下文 + 无配额记账 → 视为主动(允许)发送一次
	if err := tr.SendMedia(context.Background(), route, m); err != nil {
		t.Fatalf("主动发送应成功: %v", err)
	}
	if api.uploads != 1 || api.lastType != qqbot.FileTypeImage || api.lastBytes != 7 {
		t.Fatalf("上传异常: type=%d bytes=%d uploads=%d", api.lastType, api.lastBytes, api.uploads)
	}
	if len(api.sends) != 1 || api.sends[0].MsgType != qqbot.MsgTypeMedia || api.sends[0].Media.FileInfo != "fi-fake" {
		t.Fatalf("应发 msg_type=7 富媒体: %+v", api.sends)
	}
	if api.sends[0].MsgID != "" {
		t.Fatalf("主动路径不应带 msg_id: %+v", api.sends[0])
	}
	// 被动窗口内:带 msg_id + 自增 seq;且 file_info 缓存命中(不重复上传)
	tr.cacheReply("u1", "qqmsg-9")
	if err := tr.SendMedia(context.Background(), route, m); err != nil {
		t.Fatal(err)
	}
	if api.uploads != 1 {
		t.Fatalf("file_info 缓存应命中,不重复上传: %d", api.uploads)
	}
	last := api.sends[len(api.sends)-1]
	if last.MsgID != "qqmsg-9" || last.MsgSeq != 1 {
		t.Fatalf("被动发送应带 msg_id 与 seq=1: %+v", last)
	}
}

func TestSendMediaActiveQuotaAndGroupPath(t *testing.T) {
	api := &fakeMediaAPI{ttl: 3600}
	q := newActiveQuota("") // 纯内存配额(默认 2 条/天)
	tr, m := mediaTransport(t, api, q)
	route := im.Route{Channel: channelName, UserID: "u1", ChatID: "u1"}
	if err := tr.SendMedia(context.Background(), route, m); err != nil {
		t.Fatalf("首次主动应成功: %v", err)
	}
	if q.Remaining("qq\x00u1") != 1 {
		t.Fatalf("应扣减主动配额,余 %d", q.Remaining("qq\x00u1"))
	}
	// 配额耗尽 → 显式报错(不静默滞留)
	_ = q.Consume("qq\x00u1")
	if err := tr.SendMedia(context.Background(), route, m); err == nil ||
		!strings.Contains(err.Error(), "配额已用尽") {
		t.Fatalf("配额耗尽应显式报错: %v", err)
	}
	// 群路由:走群端点(Group 标记)
	gq := newActiveQuota("")
	tr2, m2 := mediaTransport(t, api, gq)
	if err := tr2.SendMedia(context.Background(), im.Route{Channel: channelName, UserID: "g1", ChatID: "g1", Group: true}, m2); err != nil {
		t.Fatalf("群发送应成功: %v", err)
	}
	if got := api.sendPath[len(api.sendPath)-1]; got != "group:g1" {
		t.Fatalf("群路由应走群端点: %s", got)
	}
}

func TestSendMediaFileInfoInvalidRetry(t *testing.T) {
	api := &fakeMediaAPI{ttl: 3600, sendErr: &qqbot.APIError{Code: qqbot.CodeInvalidFileInfo, Message: "bad"}}
	tr, m := mediaTransport(t, api, nil)
	route := im.Route{Channel: channelName, UserID: "u1", ChatID: "u1"}
	if err := tr.SendMedia(context.Background(), route, m); err != nil {
		t.Fatalf("file_info 失效应重传一次并成功: %v", err)
	}
	if api.uploads != 2 {
		t.Fatalf("应重传一次(共 2 次上传): %d", api.uploads)
	}
	if len(api.sends) != 2 {
		t.Fatalf("应发送两次(第二次成功): %d", len(api.sends))
	}
}

func TestSendMediaFailuresVisible(t *testing.T) {
	// 上传失败 → 显式错误 + lastError
	api := &fakeMediaAPI{uploadErr: &qqbot.APIError{Code: qqbot.CodeMediaSizeOver, Message: "too big"}}
	tr, m := mediaTransport(t, api, nil)
	route := im.Route{Channel: channelName, UserID: "u1", ChatID: "u1"}
	if err := tr.SendMedia(context.Background(), route, m); err == nil {
		t.Fatal("上传失败应报错")
	}
	tr.mu.Lock()
	le := tr.lastError
	tr.mu.Unlock()
	if !strings.Contains(le, "上传失败") {
		t.Fatalf("应记录 lastError: %q", le)
	}
	// 发送失败(非 file_info)→ 报错且不吞
	api2 := &fakeMediaAPI{sendErr: errors.New("network down")}
	tr2, m2 := mediaTransport(t, api2, nil)
	if err := tr2.SendMedia(context.Background(), route, m2); err == nil ||
		!strings.Contains(err.Error(), "富媒体") || !strings.Contains(err.Error(), "失败") {
		t.Fatalf("发送失败应显式报错: %v", err)
	}
	// 未登记产物(空 ArtifactID)/ 路径不存在 → 拒绝
	tr3, m3 := mediaTransport(t, api2, nil)
	m3.ArtifactID = ""
	if err := tr3.SendMedia(context.Background(), route, m3); err == nil {
		t.Fatal("未登记产物应拒绝")
	}
	m3.ArtifactID = "art-x"
	m3.Path = filepath.Join(t.TempDir(), "nope.png")
	if err := tr3.SendMedia(context.Background(), route, m3); err == nil {
		t.Fatal("不存在文件应报错")
	}
	// 大小不符(登记后被改)→ 拒绝
	tr4, m4 := mediaTransport(t, api2, nil)
	m4.Size += 5
	if err := tr4.SendMedia(context.Background(), route, m4); err == nil ||
		!strings.Contains(err.Error(), "与登记不符") {
		t.Fatalf("大小不符应拒绝: %v", err)
	}
}

func TestMediaCacheKeyAndCap(t *testing.T) {
	api := &fakeMediaAPI{ttl: 3600}
	tr, m := mediaTransport(t, api, nil)
	k1 := mediaCacheKey("u1", m, qqbot.FileTypeImage)
	k2 := mediaCacheKey("u2", m, qqbot.FileTypeImage)
	k3 := mediaCacheKey("u1", m, qqbot.FileTypeFile)
	if k1 == k2 || k1 == k3 {
		t.Fatalf("缓存键应区分 chat 与 file_type: %s %s %s", k1, k2, k3)
	}
	// 容量上限:插入超过上限后不超界
	for i := 0; i < mediaCacheCap+5; i++ {
		mm := m
		mm.Path = filepath.Join(t.TempDir(), "f"+string(rune('a'+i%26))+".png")
		_ = os.WriteFile(mm.Path, []byte("x"), 0o644)
		_, _ = tr.mediaFileInfo(context.Background(), api, false, "u1", mm, qqbot.FileTypeImage, []byte("x"))
	}
	tr.mu.Lock()
	n := len(tr.mediaInfos)
	tr.mu.Unlock()
	if n > mediaCacheCap {
		t.Fatalf("file_info 缓存应受容量约束: %d", n)
	}
	// TTL 覆盖:ttl=0 → 默认 1h;过期条目应重新上传
	api0 := &fakeMediaAPI{ttl: 0}
	tr2, m2 := mediaTransport(t, api0, nil)
	if _, err := tr2.mediaFileInfo(context.Background(), api0, false, "u1", m2, qqbot.FileTypeImage, []byte("x")); err != nil {
		t.Fatal(err)
	}
	tr2.mu.Lock()
	e := tr2.mediaInfos[mediaCacheKey("u1", m2, qqbot.FileTypeImage)]
	tr2.mu.Unlock()
	if time.Until(e.expiresAt) < 59*time.Minute {
		t.Fatalf("TTL 缺失时应回落 1h: %v", time.Until(e.expiresAt))
	}
	tr2.mu.Lock()
	tr2.mediaInfos[mediaCacheKey("u1", m2, qqbot.FileTypeImage)] = mediaInfoEntry{info: "stale", expiresAt: time.Now().Add(-time.Minute)}
	tr2.mu.Unlock()
	if _, err := tr2.mediaFileInfo(context.Background(), api0, false, "u1", m2, qqbot.FileTypeImage, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if api0.uploads != 2 {
		t.Fatalf("过期缓存应重传: %d", api0.uploads)
	}
}
