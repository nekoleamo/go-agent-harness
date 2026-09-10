// MED-2 端到端:base + im-qq + tool-im(data.enabled=true)真实装配 ——
// 模型经 `im_send_file` 走完整链路:产物登记账本(工作区校验)→ Bridge.SendArtifact →
// qqTransport.MediaSender → qqbot 四步分片上传(prepare → PUT 分片 → part_finish → files)
// → `msg_type=7` 富媒体消息(被动窗口内带 msg_id)。
//
// 断言:file_type 映射(png→图片 1 / txt→文件 4)、分片字节与原文一致、part_finish 回传 prepare 的 index、
// 完成请求带 upload_id、富媒体消息带被动 msg_id;越界路径显式拒绝且零出站。
package tests

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/qqbot"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// waitMediaSends 等待至少 want 条 msg_type=7 的富媒体出站消息,返回其全部快照。
func (m *qqMock) waitMediaSends(t *testing.T, want int, timeout time.Duration) []sendRec {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out := m.mediaSends()
		if len(out) >= want {
			return out
		}
		select {
		case <-m.notify:
		case <-time.After(30 * time.Millisecond):
		}
	}
	t.Fatalf("超时未收到 %d 条富媒体消息(已收 %d 条)", want, len(m.mediaSends()))
	return nil
}

// mediaSends 当前已发出的富媒体消息快照。
func (m *qqMock) mediaSends() []sendRec {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []sendRec
	for _, r := range m.sends {
		if mt, ok := r.body["msg_type"].(float64); ok && int(mt) == qqbot.MsgTypeMedia {
			out = append(out, r)
		}
	}
	return out
}

// mediaSnapshot 分片上传采集快照。
func (m *qqMock) mediaSnapshot() (preps []map[string]any, parts [][]byte, finishes, files []map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	preps = append(preps, m.mediaPrepares...)
	parts = append(parts, m.mediaParts...)
	finishes = append(finishes, m.mediaFinishes...)
	files = append(files, m.mediaFiles...)
	return
}

// sendSnapshot 当前 REST 发送总数(负例零出站断言)。
func (m *qqMock) sendSnapshot() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sends)
}

// TestImQQMediaE2E:图片(file_type=1)与文件(file_type=4)各一条完整四步上传 + 富媒体投递。
func TestImQQMediaE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.events = []map[string]any{c2cEvent(2, "qqmsg-media", "OPENID1", "远程帮我执行")}

	root := t.TempDir()
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("chart-bytes-e2e")...)
	pngPath := filepath.Join(root, "chart.png")
	if err := os.WriteFile(pngPath, png, 0o644); err != nil {
		t.Fatal(err)
	}
	txt := []byte("文本产物 e2e\n")
	txtPath := filepath.Join(root, "报告.txt")
	if err := os.WriteFile(txtPath, txt, 0o644); err != nil {
		t.Fatal(err)
	}

	_, c, _ := buildQQEnvFullSandbox(t, hs.URL, "allowlist", qqScript, []config.Entry{
		{ID: "tool-im", Data: map[string]any{"enabled": true}},
	}, root)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	if _, ok := tools.Get("im_send_file"); !ok {
		t.Fatal("im_send_file 应已注册(data.enabled=true)")
	}
	// 等入站回合完成(建立被动回复窗口 msg_id)
	if rec := m.waitSend(t, "远程命令已执行", 20*time.Second); rec.path == "" {
		t.Fatal("入站回合应有回推")
	}

	// 1) 图片:kind=image → file_type=1
	res, err := tools.Execute(context.Background(), "im_send_file",
		fmt.Sprintf(`{"target":"OPENID1","path":%q}`, pngPath))
	if err != nil || res.Error != "" {
		t.Fatalf("im_send_file(图片)应成功: err=%v res=%+v", err, res)
	}
	rec := m.waitMediaSends(t, 1, 15*time.Second)[0]
	if rec.path != "/v2/users/OPENID1/messages" {
		t.Fatalf("应发单聊路径: %q", rec.path)
	}
	if rec.body["msg_id"] != "qqmsg-media" {
		t.Fatalf("被动窗口内应带 msg_id: %v", rec.body["msg_id"])
	}
	if media, _ := rec.body["media"].(map[string]any); media == nil || media["file_info"] != "fi-e2e" {
		t.Fatalf("富媒体消息应带 file_info: %+v", rec.body)
	}

	// 2) 文件:kind=file → file_type=4(与图片共享同一上传通道)
	if _, err := tools.Execute(context.Background(), "im_send_file",
		fmt.Sprintf(`{"target":"OPENID1","path":%q}`, txtPath)); err != nil {
		t.Fatalf("im_send_file(文件)应成功: %v", err)
	}
	recs := m.waitMediaSends(t, 2, 15*time.Second)
	if media, _ := recs[1].body["media"].(map[string]any); media == nil || media["file_info"] != "fi-e2e" {
		t.Fatalf("第二条富媒体消息应带 file_info: %+v", recs[1].body)
	}

	preps, parts, finishes, files := m.mediaSnapshot()
	if len(preps) != 2 || len(parts) != 2 || len(finishes) != 2 || len(files) != 2 {
		t.Fatalf("应各两次上传步骤: preps=%d parts=%d finishes=%d files=%d", len(preps), len(parts), len(finishes), len(files))
	}
	// 3) 图片侧四步形状
	if preps[0]["file_type"] != float64(qqbot.FileTypeImage) || preps[0]["file_name"] != "chart.png" {
		t.Fatalf("图片应 file_type=1 且带文件名: %+v", preps[0])
	}
	if preps[0]["file_size"] != strconv.Itoa(len(png)) {
		t.Fatalf("file_size 应为字符串明文大小: %+v", preps[0])
	}
	sum := md5.Sum(png)
	if preps[0]["md5"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("md5 不符: %+v", preps[0])
	}
	for _, k := range []string{"sha1", "md5_10m"} {
		if s, _ := preps[0][k].(string); len(s) == 0 {
			t.Fatalf("应含 %s: %+v", k, preps[0])
		}
	}
	if string(parts[0]) != string(png) {
		t.Fatalf("分片字节应与原文一致: %q", parts[0])
	}
	if finishes[0]["upload_id"] != "up-e2e" || finishes[0]["part_index"] != float64(0) ||
		finishes[0]["block_size"] != "5242880" {
		t.Fatalf("part_finish 应回传 prepare 的 upload_id/index: %+v", finishes[0])
	}
	if files[0]["file_type"] != float64(qqbot.FileTypeImage) || files[0]["upload_id"] != "up-e2e" {
		t.Fatalf("完成请求应带 file_type + upload_id: %+v", files[0])
	}
	// 4) 文件侧类型映射差异
	if preps[1]["file_type"] != float64(qqbot.FileTypeFile) || preps[1]["file_name"] != "报告.txt" {
		t.Fatalf("txt 应 file_type=4: %+v", preps[1])
	}
	if string(parts[1]) != string(txt) {
		t.Fatalf("第二片字节应与原文一致: %q", parts[1])
	}

	// 5) 越界路径 → 显式拒绝 + 零出站
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := m.sendSnapshot()
	r2, err2 := tools.Execute(context.Background(), "im_send_file",
		fmt.Sprintf(`{"target":"OPENID1","path":%q}`, outside))
	msg := ""
	if err2 != nil {
		msg = err2.Error()
	} else if r2 != nil {
		msg = r2.Error + r2.Content
	}
	if !strings.Contains(msg, "工作区") {
		t.Fatalf("越界路径应被拒绝: err=%v res=%+v", err2, r2)
	}
	time.Sleep(400 * time.Millisecond)
	if after := m.sendSnapshot(); after != before {
		t.Fatalf("越界路径不应出站(%d → %d)", before, after)
	}
}
