package toolshell

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestExecPtyInteractive 交互式会话:sh 挂 pty,输入命令后退出,输出被采集。
func TestExecPtyInteractive(t *testing.T) {
	out, timedOut, err := execPty(context.Background(), "sh", "echo pty-ok\nexit\n")
	if err != nil {
		t.Fatal(err)
	}
	if timedOut {
		t.Fatal("交互会话不应超时")
	}
	if !strings.Contains(out, "pty-ok") {
		t.Fatalf("输出应含 pty-ok: %q", out)
	}
}

// TestExecPtyTimeout 无输入不退出 → 上下文 deadline 兜底终止,返回已捕获输出。
func TestExecPtyTimeout(t *testing.T) {
	dctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, timedOut, err := execPty(dctx, "sh", "") // sh 等待输入不退出
	if err != nil {
		t.Fatal(err)
	}
	if !timedOut {
		t.Fatal("应超时终止")
	}
	_ = out // 已捕获的部分输出(允许为空)
}

// TestShellPtyTool pty 开关开启后,shell 工具经 pty 执行并采输入结果。
func TestShellPtyTool(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{"pty": true}}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "shell", `{"command":"sh","input":"echo pty-tool-ok\nexit\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("pty shell 应成功: %s", res.Error)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatal(err)
	}
	if o, _ := out["output"].(string); !strings.Contains(o, "pty-tool-ok") {
		t.Fatalf("输出应含 pty-tool-ok: %v", out)
	}
	// timeout 标记不应出现在成功路径
	if _, has := out["timeout"]; has {
		t.Fatalf("成功路径不应有 timeout: %v", out)
	}
}

// TestShellPlainMode pty 开关默认关闭:普通模式行为不变(错误结构化回传)。
func TestShellPlainMode(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "shell", `{"command":"echo plain-ok"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" || !strings.Contains(res.Content, "plain-ok") {
		t.Fatalf("普通模式失败: err=%s content=%s", res.Error, res.Content)
	}
	res, err = tools.Execute(context.Background(), "shell", `{"command":"exit 7"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" || !strings.Contains(res.Content, "exit_error") {
		t.Fatalf("失败命令应结构化回传: err=%s content=%s", res.Error, res.Content)
	}
}