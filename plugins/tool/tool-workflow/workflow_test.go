package toolworkflow

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/adapter/llm-mock"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-fanout"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-jobs"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-llm"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-shell"
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
	// 子代理编排(M6.2)需要 ctx.llm:host-llm 提供服务,llm-mock 脚本 = 请求1 调 shell,请求2 文本收尾
	if _, err := (&hostllm.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&llmmock.Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{
		"script": `[{"tool":{"name":"shell","args":"{\"command\":\"echo sub-ok\"}"}},{"text":"子代理完成","finish":"stop"}]`,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	// 子代理编排宿主服务(M6.2 拆分):host-fanout(ctx.fanout),依赖上述 llm/tools/sysp
	if _, err := (&hostfanout.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
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

// TestAgentFanout agent:单个子代理跑独立回合并返回最终文本。
func TestAgentFanout(t *testing.T) {
	c := buildEnv(t)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "workflow", mustJSON(t, map[string]any{
		"script": `result = agent({"input": "任务A"})`,
	}))
	if err != nil || res.Error != "" {
		t.Fatalf("agent 失败: err=%v res=%+v", err, res)
	}
	if !contains(res.Content, "子代理完成") {
		t.Fatalf("agent 应返回子代理结果: %s", res.Content)
	}
}

// TestParallelFanout parallel:并发多个子代理并聚合。
func TestParallelFanout(t *testing.T) {
	c := buildEnv(t)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "workflow", mustJSON(t, map[string]any{
		"script": `result = parallel({"agents": [{"input": "甲"}, {"input": "乙"}]})`,
	}))
	if err != nil || res.Error != "" {
		t.Fatalf("parallel 失败: err=%v res=%+v", err, res)
	}
	if !contains(res.Content, "agents") {
		t.Fatalf("parallel 应返回聚合 agents: %s", res.Content)
	}
	n := strings.Count(res.Content, "子代理完成")
	if n < 2 {
		t.Fatalf("两个子代理应各返回结果: %d 次,content=%s", n, res.Content)
	}
}

// TestPipelineFanout pipeline:串行链,上一步输出作为下一步输入。
func TestPipelineFanout(t *testing.T) {
	c := buildEnv(t)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "workflow", mustJSON(t, map[string]any{
		"script": `result = pipeline({"steps": ["第一步", "第二步"]})`,
	}))
	if err != nil || res.Error != "" {
		t.Fatalf("pipeline 失败: err=%v res=%+v", err, res)
	}
	if !contains(res.Content, "steps") || !contains(res.Content, "子代理完成") {
		t.Fatalf("pipeline 应返回每步与最终结果: %s", res.Content)
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

// TestWorkflowBoundedExecution(安全审计 C4,2026-09-27):脚本由模型书写,纯计算死循环
// 必须有界 —— 此前既无步数上限也拦不住 ctx 取消(解释器只在工具调用边界看 ctx),
// `while True: pass` 会把宿主进程挂死在 100% CPU。两条断言各钉一件事:
// ① 步数上限让死循环有限时间内失败;② ctx 取消能打断解释器并显式说明原因。
func TestWorkflowBoundedExecution(t *testing.T) {
	c := buildEnv(t)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	// 顶层 while 被 starlark 语法拒绝(while loop not within a function),循环裹进函数即可
	deadLoop := mustJSON(t, map[string]any{"script": "def spin():\n  x = 0\n  while True:\n    x = x + 1\nspin()"})

	// ① 步数上限
	bounded := make(chan string, 1)
	go func() {
		res, _ := tools.Execute(context.Background(), "workflow", deadLoop)
		bounded <- res.Content
	}()
	select {
	case content := <-bounded:
		if !strings.Contains(content, "步数超过上限") {
			t.Fatalf("死循环应由步数上限中止,得 %q", content)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("死循环未被步数上限打断(宿主会被挂死)")
	}

	// ② ctx 取消打断解释器(取消必须显式回因,不能静默)。用**已取消**的 ctx:运行中取消
	// 会与步数上限抢先后(10M 步只需几十毫秒),预先取消则只有这一条路径能中止它。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := make(chan string, 1)
	go func() {
		res, _ := tools.Execute(ctx, "workflow", deadLoop)
		canceled <- res.Content
	}()
	select {
	case content := <-canceled:
		if !strings.Contains(content, "已取消") || !strings.Contains(content, "context canceled") {
			t.Fatalf("取消应显式说明原因,得 %q", content)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ctx 取消未能打断解释器")
	}
}
