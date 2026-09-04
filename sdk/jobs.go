// jobs.go:ctx.jobs 后台任务服务域模型(M6.1 host-jobs,设计 §14.1)。
package sdk

import (
	"context"
	"time"
)

// JobState 后台任务状态。
type JobState string

const (
	JobRunning JobState = "running"
	JobDone    JobState = "done"
	JobFailed  JobState = "failed"
	JobKilled  JobState = "killed"
)

// Job 一条后台任务记录。
type Job struct {
	ID        string    `json:"id"`
	State     JobState  `json:"state"`
	Command   string    `json:"command,omitempty"` // 命令任务:shell 命令行
	Output    string    `json:"output,omitempty"`  // 累计输出(命令任务 stdout+stderr)
	Result    any       `json:"result,omitempty"`  // 函数任务返回值(workflow background 对接)
	Error     string    `json:"error,omitempty"`   // 失败原因
	CreatedAt time.Time `json:"created_at"`
	DoneAt    time.Time `json:"done_at,omitempty"`
}

// JobFunc 函数型后台任务(workflow background 注册;ctx 取消即终止)。
type JobFunc func(ctx context.Context) (any, error)

// JobService 后台任务服务:提交(shell 命令/函数)→ 列表 → 取输出 → 终止。
// 提交不阻塞调用方;状态经 Output 轮询取回(M6.1 验收:不阻塞回合)。
type JobService interface {
	// Submit 提交一条 shell 命令为后台任务,立即返回任务 ID。
	Submit(cmdline string) (string, error)
	// Run 提交一个函数为后台任务(经 ctx 上下文中止),立即返回任务 ID。
	Run(fn JobFunc) (string, error)
	// List 全部任务(含历史,末位最新)。
	List() []Job
	// Output 取任务当前状态与输出。
	Output(id string) (Job, bool)
	// Kill 终止运行中的任务(killed 状态;已完成任务返回错误)。
	Kill(id string) error
}
