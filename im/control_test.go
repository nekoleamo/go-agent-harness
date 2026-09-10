// 受控出站面单测(G-E5-3 IM-1a):目标枚举 / 未授权拒绝 / 群标记 / 状态只读。
package im

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-session-log"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildControlBridge 带会话日志与授权账本的桥(用户 owner + 群 GROUP-1)。
func buildControlBridge(t *testing.T) (*Bridge, *stubTransport) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if _, err := (&sessionlog.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	tr := &stubTransport{}
	b := New(c, &stubLoop{}, sessions, tr, Options{
		Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}, AllowGroups: []string{"mock\x00GROUP-1"},
	})
	return b, tr
}

func TestControlTargetsOnlyAuthorized(t *testing.T) {
	b, _ := buildControlBridge(t)
	var svc sdk.IMControlService = b // 契约自检
	ts := svc.Targets()
	if len(ts) != 2 {
		t.Fatalf("应只枚举已授权目标(1 用户 + 1 群): %+v", ts)
	}
	if ts[0].Key != "owner" || ts[0].Group || ts[0].Channel != "mock" || !strings.Contains(ts[0].Label, "owner") {
		t.Fatalf("用户目标异常: %+v", ts[0])
	}
	if ts[1].Key != "GROUP-1" || !ts[1].Group || ts[1].ChatID != "GROUP-1" || ts[1].UserID != "" {
		t.Fatalf("群目标异常: %+v", ts[1])
	}
	// 未授权者不在枚举内(Access 可能被运行期扩容,枚举须实时)
	b.Access().Allow("mock\x00late")
	if got := svc.Targets(); len(got) != 3 {
		t.Fatalf("新授权用户应即时出现在枚举: %+v", got)
	}
}

func TestControlSendTextAuthorization(t *testing.T) {
	b, tr := buildControlBridge(t)
	var svc sdk.IMControlService = b
	// 已授权用户 → 投递到该用户(私聊 Route)
	if err := svc.SendText(context.Background(), "owner", "你好"); err != nil {
		t.Fatalf("已授权目标应可投递: %v", err)
	}
	// 带渠道前缀写法容忍
	if err := svc.SendText(context.Background(), "mock\x00owner", "你好2"); err != nil {
		t.Fatalf("channel\\x00id 写法应被归一: %v", err)
	}
	// 已授权群 → Route.Group 显式标记(通道据此走群端点)
	if err := svc.SendText(context.Background(), "GROUP-1", "群你好"); err != nil {
		t.Fatalf("已授权群应可投递: %v", err)
	}
	sent := tr.sent()
	routes := tr.routesOf()
	if len(sent) != 3 {
		t.Fatalf("应投递 3 条: %+v", sent)
	}
	if routes[0].Group || routes[0].ChatID != "owner" || routes[0].UserID != "owner" {
		t.Fatalf("私聊路由异常: %+v", routes[0])
	}
	if !routes[2].Group || routes[2].ChatID != "GROUP-1" {
		t.Fatalf("群路由应带 Group 标记: %+v", routes[2])
	}

	// 未授权/空目标/空文本 → 显式错误(不隐式回落 LastRoute)
	b.setLastRoute(Route{Channel: "mock", UserID: "owner", ChatID: "owner"})
	for _, c := range []struct{ target, text string }{
		{"stranger", "x"}, {"", "x"}, {"owner", ""}, {"owner", "   "},
	} {
		if err := svc.SendText(context.Background(), c.target, c.text); err == nil {
			t.Fatalf("target=%q text=%q 应报错", c.target, c.text)
		} else if !strings.Contains(err.Error(), "im:") {
			t.Fatalf("错误应带 im: 前缀: %v", err)
		}
	}
	if len(tr.sent()) != 3 {
		t.Fatalf("错误路径不应投递: %+v", tr.sent())
	}
}

func TestControlStatusReadonly(t *testing.T) {
	b, _ := buildControlBridge(t)
	var svc sdk.IMControlService = b
	st := svc.Status()
	if st.Channel != "mock" || st.Authorized != 1 || st.Groups != 1 {
		t.Fatalf("状态异常: %+v", st)
	}
	if st.Connected || st.Phase != "" {
		t.Fatalf("未装配 ctx.imChannels 不应谎报连通: %+v", st)
	}
	if len(st.Targets) != 2 {
		t.Fatalf("状态应带可投目标: %+v", st.Targets)
	}
	if st.Model != "" || st.Session != "" {
		t.Fatalf("未注入 llm/cwd 时应留空: %+v", st)
	}
	// busy 反映回合占用
	b.mu.Lock()
	b.busy = true
	b.mu.Unlock()
	if !svc.Status().Busy {
		t.Fatal("busy 应反映在状态里")
	}
}
