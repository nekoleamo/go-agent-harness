// MED-3 端到端:base + im-wechat + tool-im(data.enabled=true)真实装配 ——
// 模型经 `im_send_file` 走完整链路:产物登记账本(工作区校验)→ Bridge.SendArtifact →
// wechatTransport.MediaSender → **真实 *ilink.Client** 三段式(mock iLink API + mock 假 CDN:
// getuploadurl → AES-128-ECB 密文 POST → 媒体项 sendmessage);越界路径显式拒绝且零出站。
//
// 与插件单测(`plugins/ui/ui-im-wechat/media_test.go`)的分工:那层验证协议/通道形状,
// 这里验证「工具 + 桥 + 登记 + 通道 + 协议」在**真实装配**下的闭环(含 context_token 门控)。
package tests

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	imwechatb "github.com/nekoleamo/go-agent-harness/bundles/im-wechat"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/ilink"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fixedSandbox 固定工作区根(测试注入;e2e 不启用 policy-guard,故需自带 ctx.sandbox)。
type fixedSandbox struct{ root string }

func (f fixedSandbox) Mode() sdk.SandboxMode     { return sdk.SandboxWorkspace }
func (f fixedSandbox) SetMode(sdk.SandboxMode)   {}
func (f fixedSandbox) Root() string              { return f.root }
func (f fixedSandbox) ValidatePath(string) error { return nil }

// buildWechatMediaEnv base + im-wechat 装配 + 注入固定工作区根 + 额外配置条目(tool-im)。
func buildWechatMediaEnv(t *testing.T, baseURL, root string, extra []config.Entry) (*ctx.Ctx, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	store := ilink.NewStore(filepath.Join(home, "config", "ilink-wechat.yaml"))
	if err := store.Save(&ilink.Credentials{Token: "tk-e2e", BaseURL: baseURL + "/",
		AccountID: "bot", UserID: "u0", Allow: []string{"wechat\x00user1"}}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-commands"},
		{ID: "host-system-prompt"},
		{ID: "llm-mock", Data: map[string]any{"script": wechatScript}},
		{ID: "host-agent-loop"},
		{ID: "ui-im-wechat", Data: map[string]any{"base_url": baseURL + "/", "mode": "allowlist"}},
	})
	tree.Apply(extra)
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	// 固定工作区根(代替 policy-guard;出站产物登记据此判定「工作区内」)
	if err := c.Provide("ctx.sandbox", fixedSandbox{root: root}); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := imwechatb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c, home
}

// sentFileItems 已发出的「文件项」媒体消息(item_list[0].file_item)。
func (m *wechatMock) sentFileItems() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []map[string]any
	for _, b := range m.sendMsgs {
		msgv, ok := b["msg"].(map[string]any)
		if !ok {
			continue
		}
		items, ok := msgv["item_list"].([]any)
		if !ok || len(items) == 0 {
			continue
		}
		it, ok := items[0].(map[string]any)
		if !ok {
			continue
		}
		if fi, ok := it["file_item"].(map[string]any); ok {
			out = append(out, map[string]any{"item": it, "file": fi, "context_token": msgv["context_token"]})
		}
	}
	return out
}

func (m *wechatMock) sendCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sendMsgs)
}

// waitFileItem 等待文件项媒体消息到达。
func (m *wechatMock) waitFileItem(t *testing.T, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if items := m.sentFileItems(); len(items) > 0 {
			return items[0]
		}
		select {
		case <-m.notify:
		case <-time.After(30 * time.Millisecond):
		}
	}
	t.Fatalf("超时未收到文件项媒体消息(出站 %d 条)", m.sendCount())
	return nil
}

// TestImWechatMediaE2E:im_send_file → 三段式闭环(密文可解回原文)+ 越界路径零出站。
func TestImWechatMediaE2E(t *testing.T) {
	m, hs := newWechatMock(t)
	// 首条入站:授权用户 user1(建立 context_token —— 微信无主动推送,媒体投递前提)
	m.updates = []map[string]any{
		{"ret": 0, "get_updates_buf": "b1", "msgs": []map[string]any{{
			"message_type": 1, "from_user_id": "user1", "to_user_id": "bot",
			"context_token": "ct-1", "create_time_ms": int64(111),
			"item_list": []map[string]any{{"type": 1, "text_item": map[string]any{"text": "远程帮我执行"}}},
		}}},
	}
	root := t.TempDir()
	payload := []byte("出站媒体 e2e payload\n")
	file := filepath.Join(root, "报告.txt")
	if err := os.WriteFile(file, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ := buildWechatMediaEnv(t, hs.URL, root, []config.Entry{
		{ID: "tool-im", Data: map[string]any{"enabled": true}},
	})

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	if _, ok := tools.Get("im_send_file"); !ok {
		t.Fatal("im_send_file 应已注册(data.enabled=true)")
	}
	// 先等入站回合完成(回合回推文本 → 证明 context_token 已缓存)
	if got := m.waitSend(t, "远程命令已执行", 20*time.Second); got == "" {
		t.Fatal("入站回合应有回推")
	}

	// 1) 投递工作区内文件:登记 → 三段式 → 媒体项
	res, err := tools.Execute(context.Background(), "im_send_file",
		fmt.Sprintf(`{"target":"user1","path":%q}`, file))
	if err != nil || res.Error != "" {
		t.Fatalf("im_send_file 应成功: err=%v res=%+v", err, res)
	}
	rec := m.waitFileItem(t, 15*time.Second)

	// 媒体项形状(§9.2 文件项)
	item, _ := rec["item"].(map[string]any)
	fi, _ := rec["file"].(map[string]any)
	if item["type"] != float64(ilink.ItemTypeFile) {
		t.Fatalf("应为文件项(type=4): %+v", item)
	}
	if fi["file_name"] != "报告.txt" || fi["len"] != strconv.Itoa(len(payload)) {
		t.Fatalf("文件项字段不符: %+v", fi)
	}
	sum := md5.Sum(payload)
	if fi["md5"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("文件项 md5 应为明文 MD5: %+v", fi)
	}
	media, _ := fi["media"].(map[string]any)
	if media == nil || media["encrypt_query_param"] != "enc-e2e" || media["encrypt_type"] != float64(1) {
		t.Fatalf("media 形状不符: %+v", fi)
	}
	if rec["context_token"] != "ct-1" {
		t.Fatalf("应回显入站 context_token(被动窗口): %v", rec["context_token"])
	}

	// 三段式第 1/2 步:参数申请与实际上传的密文
	m.mu.Lock()
	reqs := append([]map[string]any(nil), m.uploadReqs...)
	bodies := append([][]byte(nil), m.cdnBodies...)
	queries := append([]string(nil), m.cdnQueries...)
	m.mu.Unlock()
	if len(reqs) != 1 || len(bodies) != 1 {
		t.Fatalf("应恰好一次申请 + 一次上传: reqs=%d bodies=%d", len(reqs), len(bodies))
	}
	req := reqs[0]
	if req["media_type"] != float64(ilink.MediaTypeFile) || req["to_user_id"] != "user1" {
		t.Fatalf("申请参数不符: %+v", req)
	}
	aesKey, _ := req["aeskey"].(string)
	fileKey, _ := req["filekey"].(string)
	if req["rawsize"] != float64(len(payload)) || req["filesize"] != float64(ilink.PaddedSize(int64(len(payload)))) {
		t.Fatalf("rawsize/filesize 不符: %+v(padded=%d)", req, ilink.PaddedSize(int64(len(payload))))
	}
	if req["rawfilemd5"] != hex.EncodeToString(sum[:]) || req["no_need_thumb"] != true {
		t.Fatalf("md5/no_need_thumb 不符: %+v", req)
	}
	if len(fileKey) != 32 {
		t.Fatalf("filekey 应为 16 字节 hex: %q", fileKey)
	}
	// upload_full_url 分支:URL 原样使用(保留服务端给的 token,不追加 filekey —— §9.2)
	if !strings.Contains(queries[0], "token=e2e") || strings.Contains(queries[0], "filekey=") {
		t.Fatalf("upload_full_url 应原样使用: %q", queries[0])
	}
	if media["aes_key"] != base64.StdEncoding.EncodeToString([]byte(aesKey)) {
		t.Fatalf("aes_key 应为 base64(hex): %v vs %q", media["aes_key"], aesKey)
	}
	// 密文可解回原文(端到端加密口径)
	back, derr := ilink.DecryptMedia(bodies[0], aesKey)
	if derr != nil || string(back) != string(payload) {
		t.Fatalf("CDN 密文应可解密回原文: %v %q", derr, back)
	}

	// 2) 越界路径(工作区外)→ 显式拒绝 + 零出站
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := m.sendCount()
	r2, err2 := tools.Execute(context.Background(), "im_send_file",
		fmt.Sprintf(`{"target":"user1","path":%q}`, outside))
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
	if n := m.sendCount(); n != before {
		t.Fatalf("越界路径不应出站(%d → %d)", before, n)
	}
	// 3) 通道未支持出站文件的错误口径不应再出现(回归:MED-3 已实现 MediaSender)
	if strings.Contains(msg, "不支持出站文件") {
		t.Fatalf("微信已支持出站媒体,不应报不支持: %s", msg)
	}
}
