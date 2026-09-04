package toolworkflow

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host-jobs"
	"github.com/nekoleamo/go-agent-harness/plugins/host-tools"
	"github.com/nekoleamo/go-agent-harness/plugins/tool-shell"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildEnv:host-tools + tool-shell + tool-workflow。
func buildEnv(t *testing.T) sdk.Ctx {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&toolshell.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostjobs.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWorkflowMultiStep(t *testing.T) {
	c := buildEnv(t)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	script := `r1 = shell({"command": "echo hello"})
r2 = shell({"command": "echo world"})
result = {"first": r1, "second": r2}`
	res, err := tools.Execute(context.Background(), "workflow", mustJSON(t, map[string]any{
		"script": script,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("workflow 应成功,got error: %s", res.Error)
	}
	var out map[string]map[string]any
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("结果应为 JSON: %s(%v)", res.Content, err)
	}
	first, ok := out["first"]
	if !ok {
		t.Fatalf("结果应含 first: %s", res.Content)
	}
	if s, _ := first["output"].(string); s == "" || !contains(s, "hello") {
		t.Fatalf("first 应含 echo hello 输出: %v", first)
	}
}

func TestWorkflowShellShortcutArg(t *testing.T) {
	c := buildEnv(t)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	// 单位置字符串参数 → command
	res, err := tools.Execute(context.Background(), "workflow", mustJSON(t, map[string]any{
		"script": `result = shell("echo hi")`,
	}))
	if err != nil || res.Error != "" {
		t.Fatalf("workflow 失败: err=%v res=%+v", err, res)
	}
	if !contains(res.Content, "hi") {
		t.Fatalf("结果应含 hi: %s", res.Content)
	}
}

func TestWorkflowSandboxNoStdlib(t *testing.T) {
	c := buildEnv(t)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	// 尝试读文件(无文件系统):应报错而不是成功
	res, err := tools.Execute(context.Background(), "workflow", mustJSON(t, map[string]any{
		"script": `result = open("/etc/passwd")`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Content, "undefined") && !contains(res.Content, "open") {
		t.Fatalf("应报 undefined: open(沙箱性质),got %s", res.Content)
	}
}

func TestWorkflowBackgroundAndCollect(t *testing.T) {
	c := buildEnv(t)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "workflow", mustJSON(t, map[string]any{
		"script":     `result = {"out": "bg-ok"}`,
		"background": true,
	}))
	if err != nil || res.Error != "" {
		t.Fatalf("background 失败: err=%v res=%+v", err, res)
	}
	var job struct {
		JobID  string                 `json:"job_id"`
		Result map[string]interface{} `json:"result"`
	}
	if err := json.Unmarshal([]byte(res.Content), &job); err != nil {
		t.Fatalf("job 响应解析失败: %s", res.Content)
	}
	if job.JobID == "" {
		t.Fatal("应返回 job_id")
	}
	// 真异步(M6.1):collect 轮询直到完成
	deadline := time.Now().Add(5 * time.Second)
	for {
		col, err := tools.Execute(context.Background(), "workflow_collect", mustJSON(t, map[string]any{"job_id": job.JobID}))
		if err != nil {
			t.Fatal(err)
		}
		if col.Error == "" && contains(col.Content, "bg-ok") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("collect 超时未取到结果: %s %s", col.Error, col.Content)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
