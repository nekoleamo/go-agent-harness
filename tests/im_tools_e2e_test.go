// G-E5-3(IM-1)端到端:base + im-qq + tool-im(data.enabled=true)真实装配 ——
// 工具对模型可见 → im_status 给出已授权目标 → im_send 经 QQ 栈真实投递(被动窗口 msg_id)→
// 未授权目标显式拒绝且零出站(不隐式回落 LastRoute)。
package tests

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestImToolsE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.events = []map[string]any{c2cEvent(2, "qqmsg-tools", "OPENID1", "远程帮我执行")}
	_, c, _ := buildQQEnvFull(t, hs.URL, "allowlist", qqScript, []config.Entry{
		{ID: "tool-im", Data: map[string]any{"enabled": true}},
	})

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	// 1) 启用后工具对模型可见
	for _, name := range []string{"im_send", "im_status"} {
		if _, ok := tools.Get(name); !ok {
			t.Fatalf("%s 应已注册(data.enabled=true)", name)
		}
	}

	// 2) 等首条入站回合完成(建立被动回复上下文)
	if rec := m.waitSend(t, "远程命令已执行", 20*time.Second); rec.path == "" {
		t.Fatal("入站回合应产生被动回复")
	}

	// 3) im_status:反映渠道与已授权目标(OPENID1)
	st, err := tools.Execute(context.Background(), "im_status", "{}")
	if err != nil || st.Error != "" {
		t.Fatalf("im_status 失败: err=%v res=%+v", err, st)
	}
	if !strings.Contains(st.Content, "OPENID1") || !strings.Contains(st.Content, `"channel":"qq"`) {
		t.Fatalf("im_status 应含已授权目标与渠道: %s", st.Content)
	}

	// 4) im_send → 真实经 QQ REST 投递(被动窗口内带 msg_id)
	res, err := tools.Execute(context.Background(), "im_send", `{"target":"OPENID1","text":"来自模型的回执"}`)
	if err != nil || res.Error != "" {
		t.Fatalf("im_send 应成功: err=%v res=%+v", err, res)
	}
	rec := m.waitSend(t, "来自模型的回执", 10*time.Second)
	if rec.path != "/v2/users/OPENID1/messages" {
		t.Fatalf("应发单聊路径,得 %q", rec.path)
	}
	if rec.body["msg_id"] != "qqmsg-tools" {
		t.Fatalf("被动窗口内应带 msg_id,得 %v", rec.body["msg_id"])
	}

	// 5) 未授权目标:显式拒绝 + 零出站
	before := m.textSendCount()
	r2, err := tools.Execute(context.Background(), "im_send", `{"target":"OPENID-STRANGER","text":"越权"}`)
	msg := ""
	if err != nil {
		msg = err.Error()
	} else if r2 != nil {
		msg = r2.Error + r2.Content
	}
	if !strings.Contains(msg, "未授权") {
		t.Fatalf("未授权目标应被拒绝: err=%v res=%+v", err, r2)
	}
	time.Sleep(600 * time.Millisecond)
	if n := m.textSendCount(); n != before {
		t.Fatalf("未授权目标不应出站(出站数 %d → %d)", before, n)
	}
}

// 默认关闭:未配置 tool-im 时模型看不到这两个工具(默认不注册口径)。
func TestImToolsDefaultOffE2E(t *testing.T) {
	m, hs := newQQMock(t)
	_ = m
	_, c, _ := buildQQEnvFull(t, hs.URL, "allowlist", qqScript, nil)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"im_send", "im_status"} {
		if _, ok := tools.Get(name); ok {
			t.Fatalf("默认(未启用)不应注册 %s", name)
		}
	}
}
