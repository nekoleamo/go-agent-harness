// export.go:导出 starlark 执行引擎 API(供 extplugins/tool/tool-workflow 外部化进程复用)。
// 外部化(M6.8):引擎不绑定宿主,工具/后台任务/子代理经注入接口驱动——宿主内嵌插件注入
// 真实服务,外部进程注入 host-bridge 回调代理(CbTools/CbJobs/CbFanout),同一套引擎两处复用。
package toolworkflow

import (
	"context"
	"log/slog"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// NewExecutor 构造 starlark 执行引擎(tools/jobs/fanout 可为回调代理;logger 输出脚本 print)。
func NewExecutor(tools sdk.ToolRegistry, jobs sdk.JobService, fanout sdk.FanoutService, logger *slog.Logger, c sdk.Ctx) *WorkflowTool {
	return &WorkflowTool{tools: tools, jobsSvc: jobs, fanout: fanout, logger: logger, c: c}
}

// Run 执行脚本并返回 result 值(导出:外部进程直接调用)。
func (w *WorkflowTool) Run(ctx context.Context, script string) (any, error) {
	return w.run(ctx, script)
}

// JobsService 取后台任务服务(外部进程实现 workflow_collect 时经回调代理注入)。
func (w *WorkflowTool) JobsService() sdk.JobService { return w.jobsSvc }

// Ctx 返回注入的上下文(外部进程作日志/发射通道;宿主内嵌不经此用)。
func (w *WorkflowTool) Ctx() sdk.Ctx { return w.c }

// CollectResult 通用后台任务收集(导出:外部进程与内嵌 Collector 共用同一语义)。
func CollectResult(ctx context.Context, jobs sdk.JobService, jobID string) any {
	if jobs == nil {
		return map[string]any{"error": "ctx.jobs 未装配(host-jobs)"}
	}
	job, ok := jobs.Output(jobID)
	if !ok {
		return map[string]any{"error": "job 不存在: " + jobID}
	}
	if job.State == sdk.JobRunning {
		return map[string]any{"job_id": jobID, "state": "running"}
	}
	if job.State != sdk.JobDone {
		return map[string]any{"job_id": jobID, "state": string(job.State), "error": job.Error}
	}
	return map[string]any{"job_id": jobID, "result": job.Result}
}
