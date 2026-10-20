package hostbridge

// 跨进程进度回传的用例(2026-10-20 C1)。
//
// 要钉的是归属与安全两条:
//  1. 宿主发放令牌 → 登记归属 → 回传的块挂到**那一次调用**上;
//  2. 令牌注销后(或令牌非法)回传的块**静默丢弃** —— 插件不能伪造别的调用的归属,
//     迟到的块也不能挂到已经结束的工具上。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestProgressTokenOwnership 令牌 → 归属:块挂到登记时的那次调用上。
func TestProgressTokenOwnership(t *testing.T) {
	b := &Bridge{}
	var mu sync.Mutex
	var got []string
	rep := func(tool, chunk string) error {
		mu.Lock()
		got = append(got, tool+":"+chunk)
		mu.Unlock()
		return nil
	}

	b.registerProgress("tok-1", rep, "shell")
	if err := b.handleProgress("tok-1", "编译中"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "shell:编译中" {
		t.Fatalf("块应挂到登记的归属上,got %v", got)
	}
}

// TestProgressUnknownTokenDropped 令牌查不到 ⇒ 静默丢弃(不报错、不张冠李戴)。
func TestProgressUnknownTokenDropped(t *testing.T) {
	b := &Bridge{}
	called := false
	b.registerProgress("tok-1", func(string, string) error { called = true; return nil }, "shell")
	if err := b.handleProgress("tok-2", "别人的块"); err != nil {
		t.Fatalf("未知令牌不应报错,got %v", err)
	}
	if called {
		t.Fatal("未知令牌的块不得挂到任何调用上")
	}
}

// TestProgressAfterUnregisterDropped 调用结束(注销)后迟到的块无处可挂 ⇒ 丢弃。
func TestProgressAfterUnregisterDropped(t *testing.T) {
	b := &Bridge{}
	called := false
	b.registerProgress("tok-1", func(string, string) error { called = true; return nil }, "shell")
	b.unregisterProgress("tok-1")
	if err := b.handleProgress("tok-1", "迟到的块"); err != nil {
		t.Fatalf("迟到块不应报错,got %v", err)
	}
	if called {
		t.Fatal("注销后的块不得再挂到调用上")
	}
}

// TestProgressEmptyTokenIgnored 空令牌/空块 ⇒ 忽略(不产生噪音)。
func TestProgressEmptyTokenIgnored(t *testing.T) {
	b := &Bridge{}
	if err := b.handleProgress("", "x"); err != nil {
		t.Fatal(err)
	}
	b.registerProgress("tok-1", func(string, string) error { return nil }, "shell")
	if err := b.handleProgress("tok-1", ""); err != nil {
		t.Fatal(err)
	}
}

// TestProgressReporterWithoutToken 宿主没开进度通道(老宿主)⇒ 上报出口为 nil,
// 工具照常工作(不能因为没有进度通道就让工具失败)。
func TestProgressReporterWithoutToken(t *testing.T) {
	if rep := progressReporter(""); rep != nil {
		t.Fatal("无令牌时不应给出上报出口")
	}
	rep := progressReporter("tok-1")
	if rep == nil {
		t.Fatal("有令牌时应给出上报出口")
	}
	// 宿主没注入回调通道地址 ⇒ 回传失败,但只返回错误(工具侧忽略),不 panic。
	t.Setenv("GAH_CB_ADDR", "")
	if err := rep("shell", "x"); err == nil {
		t.Fatal("无回调通道时应回错误(由调用方忽略)")
	}
}

// TestProgressRecordPayloadRoundTrip 回传载荷的序列化/反序列化必须字段名一致
// (跨进程两端的 JSON 键对不上 ⇒ 块全丢,且不报错 ⇒ 界面永远没输出)。
func TestProgressRecordPayloadRoundTrip(t *testing.T) {
	raw, err := marshalProgressRecord("tok-9", "增量")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"token":"tok-9"`) || !strings.Contains(raw, `"chunk":"增量"`) {
		t.Fatalf("载荷字段名应稳定,got %s", raw)
	}
}

// TestProgressReporterContext 外部进程侧:上报出口装进 ctx 后,工具经
// sdk.ReportToolProgress 触达回调通道(没装 ctx 时静默跳过)。
func TestProgressReporterContext(t *testing.T) {
	t.Setenv("GAH_CB_ADDR", "")
	rep := progressReporter("tok-1")
	ctx := sdk.WithToolProgressReporter(context.Background(), rep)
	// 没有回调通道 ⇒ 回传失败但 sdk.ReportToolProgress 忽略错误(不 panic、不影响工具)。
	sdk.ReportToolProgress(ctx, "shell", "x")
}

// TestProgressRegisterNoop 空令牌/空出口 ⇒ 不登记(没有可归属的调用)。
func TestProgressRegisterNoop(t *testing.T) {
	b := &Bridge{}
	b.registerProgress("", func(string, string) error { return nil }, "shell")
	b.registerProgress("tok", nil, "shell")
	if len(b.progOwners) != 0 {
		t.Fatalf("不该登记任何归属,got %d", len(b.progOwners))
	}
	b.unregisterProgress("")
}

// TestProgressHandlerPropagatesError 上报出口报错时如实回传(不吞)。
func TestProgressHandlerPropagatesError(t *testing.T) {
	b := &Bridge{}
	b.registerProgress("tok-1", func(string, string) error { return errors.New("上报失败") }, "shell")
	if err := b.handleProgress("tok-1", "x"); err == nil {
		t.Fatal("上报出口的错误应如实回传")
	}
}

// TestCallbackProgressRecord 回调通道的 progress/record:载荷解析 + 归属还原。
func TestCallbackProgressRecord(t *testing.T) {
	cb := NewCallback(&cbStubTools{}, nil, nil, "")
	var reply string
	// 未装配出口 ⇒ 显式报错(外部工具的增量不能被静默吞掉)。
	if err := cb.Call(CallArgs{Service: progressService, Method: progressRecord, Args: `{"token":"t","chunk":"x"}`}, &reply); err == nil {
		t.Fatal("未装配进度出口时应显式报错")
	}
	var got []string
	cb.SetProgressSink(func(token, chunk string) error {
		got = append(got, token+":"+chunk)
		return nil
	})
	// 未知方法 / 坏载荷 / 缺令牌 ⇒ 各自显式报错。
	for _, tc := range []struct{ name, method, args string }{
		{"未知方法", "push", `{"token":"t","chunk":"x"}`},
		{"坏载荷", progressRecord, `{`},
		{"缺令牌", progressRecord, `{"chunk":"x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := cb.Call(CallArgs{Service: progressService, Method: tc.method, Args: tc.args}, &reply); err == nil {
				t.Fatal("应显式报错")
			}
		})
	}
	if err := cb.Call(CallArgs{Service: progressService, Method: progressRecord, Args: `{"token":"t1","chunk":"编译中"}`}, &reply); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "t1:编译中" {
		t.Fatalf("回传载荷应还原到宿主出口,got %v", got)
	}
}

// TestProgressSenderRoundTrip 外部进程侧:经回调通道把增量送回宿主(端到端)。
func TestProgressSenderRoundTrip(t *testing.T) {
	cb := NewCallback(&cbStubTools{}, nil, nil, "")
	var got []string
	cb.SetProgressSink(func(token, chunk string) error {
		got = append(got, token+":"+chunk)
		return nil
	})
	addr, stop, err := serveCallback(cb)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	t.Setenv("GAH_CB_ADDR", addr)
	t.Setenv("GAH_CB_TOKEN", "")

	s := &progressSender{}
	if err := s.send("tok-9", "增量"); err != nil {
		t.Fatalf("回传应成功: %v", err)
	}
	if len(got) != 1 || got[0] != "tok-9:增量" {
		t.Fatalf("宿主应收到增量,got %v", got)
	}
}

// TestProgressSenderNoAddr 宿主没注入地址 ⇒ 显式报错(外部工具仍照常工作)。
func TestProgressSenderNoAddr(t *testing.T) {
	t.Setenv("GAH_CB_ADDR", "")
	s := &progressSender{}
	if err := s.send("tok", "x"); err == nil {
		t.Fatal("无地址时应报错")
	}
}

// TestProgressSenderDialFailure 拨号失败后不再重试(回调通道没开时反复拨号只是噪音)。
func TestProgressSenderDialFailure(t *testing.T) {
	t.Setenv("GAH_CB_ADDR", "127.0.0.1:1") // 无人监听
	s := &progressSender{}
	if err := s.send("tok", "x"); err == nil {
		t.Fatal("拨号失败应报错")
	}
	err2 := s.send("tok", "x")
	if err2 == nil || !strings.Contains(err2.Error(), "不再回传") {
		t.Fatalf("拨号失败后应停止重试,got %v", err2)
	}
}
