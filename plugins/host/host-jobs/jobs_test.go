package hostjobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/plugins/policy/policy-guard"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// waitDone 轮询任务直到非 running(deadline 内)。
func waitDone(t *testing.T, j *Jobs, id string) sdk.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, ok := j.Output(id)
		if !ok {
			t.Fatalf("任务 %s 不存在", id)
		}
		if job.State != sdk.JobRunning {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("任务 %s 超时未完成(state=%s)", id, job.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestSubmitCommandDone 命令任务:提交 → 完成 → 输出正确。
func TestSubmitCommandDone(t *testing.T) {
	j := New(nil)
	id, err := j.Submit(`echo "你好,后台任务"`)
	if err != nil {
		t.Fatal(err)
	}
	job := waitDone(t, j, id)
	if job.State != sdk.JobDone {
		t.Fatalf("应为 done,got %s(err=%s)", job.State, job.Error)
	}
	if job.Output == "" || job.Output[len(job.Output)-1] != '\n' {
		t.Fatalf("输出意外: %q", job.Output)
	}
	if job.Command == "" {
		t.Fatal("应记录命令")
	}
	if job.DoneAt.IsZero() {
		t.Fatal("应记录完成时间")
	}
	// List 应含该任务
	list := j.List()
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("List 不符: %+v", list)
	}
}

// TestSubmitCommandFailed 失败命令 → failed 状态带错误。
func TestSubmitCommandFailed(t *testing.T) {
	j := New(nil)
	id, err := j.Submit(`exit 3`)
	if err != nil {
		t.Fatal(err)
	}
	job := waitDone(t, j, id)
	if job.State != sdk.JobFailed {
		t.Fatalf("应为 failed,got %s", job.State)
	}
	if job.Error == "" {
		t.Fatal("应有错误信息")
	}
}

// TestKillRunning 终止运行中的任务 → killed 且进程被杀。
func TestKillRunning(t *testing.T) {
	j := New(nil)
	id, err := j.Submit(`sleep 60`)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Kill(id); err != nil {
		t.Fatalf("Kill 失败: %v", err)
	}
	job := waitDone(t, j, id)
	if job.State != sdk.JobKilled {
		t.Fatalf("应为 killed,got %s", job.State)
	}
	// 再次 Kill 已完成任务 → 报错
	if err := j.Kill(id); err == nil {
		t.Fatal("已完成任务 Kill 应报错")
	}
	// 不存在任务 Kill → 报错
	if err := j.Kill("job_99999"); err == nil {
		t.Fatal("不存在任务 Kill 应报错")
	}
}

// TestRunFuncTask 函数任务:成功 → Result 取回;失败 → failed。
func TestRunFuncTask(t *testing.T) {
	j := New(nil)
	id, err := j.Run(func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true, "n": 42}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	job := waitDone(t, j, id)
	if job.State != sdk.JobDone {
		t.Fatalf("函数任务应为 done,got %s", job.State)
	}
	if m, ok := job.Result.(map[string]any); !ok || m["ok"] != true || m["n"] != 42 {
		t.Fatalf("Result 不符: %v", job.Result)
	}

	badID, err := j.Run(func(ctx context.Context) (any, error) {
		return nil, errors.New("函数执行失败")
	})
	if err != nil {
		t.Fatal(err)
	}
	bad := waitDone(t, j, badID)
	if bad.State != sdk.JobFailed || bad.Error == "" {
		t.Fatalf("失败任务状态不符: state=%s", bad.State)
	}

	// nil 函数 → 提交报错
	if _, err := j.Run(nil); err == nil {
		t.Fatal("nil 函数应报错")
	}
}

// TestListAndHistory 完成的超 keepHistory 条后最旧的被清理;运行中的保留。
func TestListAndHistory(t *testing.T) {
	j := New(nil)
	runningID, err := j.Submit(`sleep 5`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < keepHistory; i++ {
		id, err := j.Submit(`echo x`)
		if err != nil {
			t.Fatal(err)
		}
		waitDone(t, j, id)
	}
	list := j.List()
	if len(list) != keepHistory+1 {
		t.Fatalf("列表长度: got %d, want %d(运行中不清理)", len(list), keepHistory+1)
	}
	// 运行中任务完成 → 触发清理:最旧完成(runningID)被清,总长 ≤ keepHistory
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, ok := j.Output(runningID) // 完成可能已被清理(不存在=已清),都视为推进
		if !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runningID 未完成也未清理")
		}
		time.Sleep(50 * time.Millisecond)
	}
	list = j.List()
	if len(list) != keepHistory {
		t.Fatalf("历史应限 keepHistory: got %d, want %d", len(list), keepHistory)
	}
}

// TestJobDoneEventNotify 终态通知:done/failed/killed 各恰一次(ID+State)。
func TestJobDoneEventNotify(t *testing.T) {
	j := New(nil)
	var mu sync.Mutex
	var evs []sdk.JobDoneEvent
	notified := make(chan struct{}, 8)
	j.SetNotify(func(ev sdk.JobDoneEvent) {
		mu.Lock()
		evs = append(evs, ev)
		mu.Unlock()
		notified <- struct{}{}
	})
	// done
	id1, err := j.Run(func(context.Context) (any, error) { return "ok", nil })
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, j, id1)
	// failed
	id2, err := j.Run(func(context.Context) (any, error) { return nil, errors.New("boom") })
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, j, id2)
	// killed:阻塞函数任务经 Kill 终止
	id3, err := j.Run(func(ctx context.Context) (any, error) { <-ctx.Done(); return nil, ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Kill(id3); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-notified:
		case <-time.After(5 * time.Second):
			t.Fatal("终态通知缺失")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(evs) != 3 {
		t.Fatalf("应收到 3 次通知,got %d", len(evs))
	}
	byID := map[string]sdk.JobDoneEvent{}
	for _, ev := range evs {
		byID[ev.ID] = ev
	}
	if s := byID[id1].State; s != sdk.JobDone {
		t.Errorf("%s 应为 done,got %s", id1, s)
	}
	if s := byID[id2].State; s != sdk.JobFailed {
		t.Errorf("%s 应为 failed,got %s", id2, s)
	}
	if s := byID[id3].State; s != sdk.JobKilled {
		t.Errorf("%s 应为 killed,got %s", id3, s)
	}
}

// TestReadOnlySandbox 拒绝提交:对齐 policy-guard read-only 档。
func TestReadOnlySandbox(t *testing.T) {
	sb := policyguard.DefaultSandbox("/tmp/ws")
	sb.SetMode(sdk.SandboxReadOnly)
	j := New(sb)
	if _, err := j.Submit(`echo hi`); err == nil {
		t.Fatal("read-only 下 Submit 应被拒绝")
	}
	if _, err := j.Run(func(ctx context.Context) (any, error) { return nil, nil }); err == nil {
		t.Fatal("read-only 下 Run 应被拒绝")
	}
	// workspace-write 放行
	sb.SetMode(sdk.SandboxWorkspace)
	if _, err := j.Submit(`echo hi`); err != nil {
		t.Fatalf("workspace-write 下 Submit 应放行: %v", err)
	}
}
