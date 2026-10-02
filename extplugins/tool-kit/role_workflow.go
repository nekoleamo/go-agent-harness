// 本文件:toolkit 二进制里的 tool-workflow 角色。
//
// 2026-10-02 瘦身:原来 tool-basic / tool-workflow / tool-mcp / tool-subagent 是**四个**
// 独立二进制,各自静态链一遍运行时 + host-bridge + sdk —— 实测四份合计 30.8 MiB,
// 而合成一个只要 9.6 MiB(省 69%)。合成的是**二进制**,不是**进程**:宿主仍按角色逐个
// 起进程(崩溃隔离不丢),配置文件里的插件 id 一个都没变。
//
// 自合并以来本文件内容**逐字未改**,只把 main() 改成由 toolkit 分派调用。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	bridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-workflow"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 由 tool-kit 的分派调用(见 main.go)。
func runWorkflow() {
	if v, ok := os.LookupEnv("GAH_PLUGIN"); !ok || v != "gah-external-tool" {
		fmt.Fprintln(os.Stderr, "外部插件缺少握手标识 GAH_PLUGIN")
		os.Exit(1)
	}
	cbAddr := os.Getenv("GAH_CB_ADDR")
	if cbAddr == "" {
		fmt.Fprintln(os.Stderr, "tool-workflow: 缺少 GAH_CB_ADDR(宿主未开启回调通道)")
		os.Exit(1)
	}
	cc, err := bridge.DialCallback(cbAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tool-workflow: 连接宿主回调失败:", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	exec := toolworkflow.NewExecutor(
		bridge.CbTools(cc),  // 工具执行/列表 → 宿主
		bridge.CbJobs(cc),   // 后台任务状态 → 宿主
		bridge.CbFanout(cc), // 子代理编排 → 宿主
		logger, nil,         // 宿主内嵌传 sdk.Ctx;外部进程用 nil(引擎不依赖)
	)
	bridge.ServeTools(map[string]sdk.Tool{
		"workflow":         &wfTool{exec: exec, cc: cc},
		"workflow_collect": &wfCollect{cc: cc},
	})
}

// wfTool workflow 工具(外部实现)。
type wfTool struct {
	exec *toolworkflow.WorkflowTool
	cc   *bridge.CallbackClient
}

func (t *wfTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "workflow",
		Description: "用受限 starlark 程序一把过组合多步工具调用;每个可用工具以同名函数暴露;顶层变量 result 即结果;background:true 异步。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"script":     map[string]any{"type": "string"},
				"background": map[string]any{"type": "boolean"},
			},
			"required": []any{"script"},
		},
	}
}

func (t *wfTool) Execute(ctx context.Context, raw string) (any, error) {
	var a struct {
		Script     string `json:"script"`
		Background bool   `json:"background,omitempty"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("workflow: args: %w", err)
	}
	if strings.TrimSpace(a.Script) == "" {
		return nil, fmt.Errorf("workflow: 缺少 script")
	}
	if a.Background {
		// 真异步:回调宿主 jobs.run——宿主起后台任务,任务体 = 经桥协议调回本工具(非 background),
		// 由宿主 jobs 托管生命周期;结果经 workflow_collect(回调 jobs.output)轮询。
		argsJSON, _ := json.Marshal(map[string]any{"script": a.Script})
		var jobID string
		if err := t.cc.Call("jobs", "run", map[string]string{"Name": "workflow", "Args": string(argsJSON)}, &jobID); err != nil {
			return map[string]any{"error": "后台任务提交失败: " + err.Error()}, nil
		}
		return map[string]any{"background": true, "job_id": jobID}, nil
	}
	result, err := t.exec.Run(ctx, a.Script)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	return result, nil
}

// wfCollect workflow_collect 工具(外部实现):回调宿主 jobs.output。
type wfCollect struct {
	cc *bridge.CallbackClient
}

func (t *wfCollect) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "workflow_collect",
		Description: "取回 background workflow 的结果:{job_id}",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"job_id"},
		},
	}
}

func (t *wfCollect) Execute(ctx context.Context, raw string) (any, error) {
	var a struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, err
	}
	var jobJSON string
	if err := t.cc.Call("jobs", "output", map[string]string{"ID": a.JobID}, &jobJSON); err != nil {
		return map[string]any{"error": "取回任务失败: " + err.Error()}, nil
	}
	var job sdk.Job
	if err := json.Unmarshal([]byte(jobJSON), &job); err != nil {
		return map[string]any{"error": "任务状态解析失败"}, nil
	}
	// 同 CollectResult 语义(外部进程无法直接注入 sdk.JobService,手工组装)
	if job.State == sdk.JobRunning {
		return map[string]any{"job_id": a.JobID, "state": "running"}, nil
	}
	if job.State != sdk.JobDone {
		return map[string]any{"job_id": a.JobID, "state": string(job.State), "error": job.Error}, nil
	}
	return map[string]any{"job_id": a.JobID, "result": job.Result}, nil
}
