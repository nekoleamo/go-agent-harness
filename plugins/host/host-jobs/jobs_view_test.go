// jobs_view_test.go S-P0-3:/jobs 统一视图(任务 + 子代理)纯逻辑单测。
// 覆盖排序(运行中优先)、耗时口径、id 枚举(全量/仅运行中)、输出回退(输出→结果)、
// 子代理输出(结果 + 对话尾部)与截断保护。
package hostjobs

import (
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

type viewJobsStub struct {
	jobs    []sdk.Job
	killed  string
	killErr error
}

func (s *viewJobsStub) Submit(string) (string, error)   { return "", nil }
func (s *viewJobsStub) Run(sdk.JobFunc) (string, error) { return "", nil }
func (s *viewJobsStub) List() []sdk.Job                 { return s.jobs }
func (s *viewJobsStub) Output(id string) (sdk.Job, bool) {
	for _, j := range s.jobs {
		if j.ID == id {
			return j, true
		}
	}
	return sdk.Job{}, false
}
func (s *viewJobsStub) Kill(id string) error { s.killed = id; return s.killErr }

type viewFanoutStub struct {
	sdk.FanoutService
	agents []sdk.AgentHandle
	killed string
}

func (s *viewFanoutStub) ListAgents() []sdk.AgentHandle { return s.agents }
func (s *viewFanoutStub) AgentStatus(id string) (sdk.AgentHandle, bool) {
	for _, a := range s.agents {
		if a.ID == id {
			return a, true
		}
	}
	return sdk.AgentHandle{}, false
}
func (s *viewFanoutStub) KillAgent(id string) error { s.killed = id; return nil }

func viewFixture() (*viewJobsStub, *viewFanoutStub) {
	now := time.Now()
	js := &viewJobsStub{jobs: []sdk.Job{
		{ID: "j1", State: sdk.JobDone, Command: "go build ./...", CreatedAt: now.Add(-2 * time.Minute), DoneAt: now.Add(-90 * time.Second)},
		{ID: "j2", State: sdk.JobRunning, Command: "npm test --watch", CreatedAt: now.Add(-12 * time.Second)},
		{ID: "j3", State: sdk.JobFailed, Command: "make x", Error: "exit 2", CreatedAt: now.Add(-5 * time.Minute), DoneAt: now.Add(-4 * time.Minute)},
		{ID: "j4", State: sdk.JobDone, Result: map[string]any{"ok": true}, CreatedAt: now.Add(-6 * time.Minute), DoneAt: now.Add(-5 * time.Minute)},
	}}
	fo := &viewFanoutStub{agents: []sdk.AgentHandle{
		{ID: "a1", State: sdk.AgentRunning, Input: "重构 auth 模块", CreatedAt: now.Add(-8 * time.Second)},
		{ID: "a2", State: sdk.AgentDone, Input: "分析日志", Result: "结论: 无异常", CreatedAt: now.Add(-3 * time.Minute)},
	}}
	return js, fo
}

func TestJobsListView(t *testing.T) {
	js, fo := viewFixture()
	out := jobsListView(js, fo)
	for _, want := range []string{
		"后台任务与子代理:2 运行中 / 6 条记录",
		"[job] j2", "[agent] a1", "[job] j1", "[agent] a2",
		"npm test --watch", "重构 auth 模块",
		"exit 2",       // 失败原因随摘要
		"map[ok:true]", // 函数任务用结果做摘要
		"用法:/jobs output <id>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
	// 运行中(含子代理)排前:两个 running 行必须早于任何非 running 行
	firstDone := len(out)
	for _, id := range []string{"j1", "j3", "j4", "a2"} {
		if i := strings.Index(out, id); i >= 0 && i < firstDone {
			firstDone = i
		}
	}
	for _, id := range []string{"j2", "a1"} {
		if i := strings.Index(out, id); i > firstDone {
			t.Errorf("运行中项 %s 未排在已完成项之前:\n%s", id, out)
		}
	}
}

func TestJobsListViewEmptyAndNoFanout(t *testing.T) {
	if out := jobsListView(&viewJobsStub{}, nil); !strings.Contains(out, "均无记录") {
		t.Errorf("空列表应显式说明: %q", out)
	}
	js, _ := viewFixture()
	if out := jobsListView(js, nil); strings.Contains(out, "[agent]") || !strings.Contains(out, "[job] j1") {
		t.Errorf("无 ctx.fanout 应退化为纯任务视图: %q", out)
	}
}

func TestJobsListViewCap(t *testing.T) {
	now := time.Now()
	js := &viewJobsStub{}
	for i := 0; i < 20; i++ {
		js.jobs = append(js.jobs, sdk.Job{ID: "j" + string(rune('a'+i)), State: sdk.JobDone, Command: "c", CreatedAt: now.Add(-time.Duration(i) * time.Second), DoneAt: now})
	}
	out := jobsListView(js, nil)
	if !strings.Contains(out, "…另 8 条") {
		t.Errorf("超 12 行应省略: %q", out)
	}
}

func TestJobsOutputView(t *testing.T) {
	js, fo := viewFixture()
	js.jobs[0].Output = "line1\nline2\n"
	out, err := jobsOutputView(js, fo, "j1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"任务 j1", "go build ./...", "line1", "line2"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
	// 无 Output 的函数任务回退 Result
	out, err = jobsOutputView(js, fo, "j4")
	if err != nil || !strings.Contains(out, "map[ok:true]") {
		t.Errorf("函数任务应回退结果: %v %q", err, out)
	}
	// 运行中任务耗时来自 now-Created(有输出才不显示「暂无」)
	if out, _ := jobsOutputView(js, fo, "j2"); !strings.Contains(out, "暂无输出") {
		t.Errorf("无输出应显式说明: %q", out)
	}
	// 未知 id 显式报错(任务与子代理都没有)
	if _, err := jobsOutputView(js, fo, "nope"); err == nil || !strings.Contains(err.Error(), "未找到任务/子代理") {
		t.Errorf("未知 id 应报错: %v", err)
	}
}

func TestJobsOutputViewAgent(t *testing.T) {
	js, fo := viewFixture()
	fo.agents[1].Messages = []sdk.AgentMessage{
		{From: "user", Content: "再看下 p99"},
		{From: "agent", Content: "已补"},
	}
	out, err := jobsOutputView(js, fo, "a2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"子代理 a2", "分析日志", "结论: 无异常", "user: 再看下 p99", "agent: 已补"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
	// 无任务服务时纯子代理视图可用
	if out, err := jobsOutputView(nil, fo, "a2"); err != nil || !strings.Contains(out, "子代理 a2") {
		t.Errorf("无 ctx.jobs 时应仍可看子代理: %v %q", err, out)
	}
}

func TestJobsOutputViewAgentMessageCap(t *testing.T) {
	_, fo := viewFixture()
	h := sdk.AgentHandle{ID: "a9", State: sdk.AgentRunning, Input: "长对话"}
	for i := 0; i < 20; i++ {
		h.Messages = append(h.Messages, sdk.AgentMessage{From: "user", Content: "m"})
	}
	fo.agents = append(fo.agents, h)
	out, err := jobsOutputView(nil, fo, "a9")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "对话(20 条,末 8)") {
		t.Errorf("对话应只列尾部 8 条: %q", out)
	}
}

func TestJobsKillView(t *testing.T) {
	js, fo := viewFixture()
	if out, err := jobsKillView(js, fo, "j2"); err != nil || !strings.Contains(out, "已终止任务 j2") {
		t.Fatalf("任务终止失败: %v %q", err, out)
	}
	if js.killed != "j2" {
		t.Errorf("未调用 JobService.Kill: %q", js.killed)
	}
	// 非任务 id 回退子代理
	if out, err := jobsKillView(js, fo, "a1"); err != nil || !strings.Contains(out, "已终止子代理 a1") {
		t.Fatalf("子代理终止失败: %v %q", err, out)
	}
	if fo.killed != "a1" {
		t.Errorf("未调用 FanoutService.KillAgent: %q", fo.killed)
	}
	// 未知 id
	if _, err := jobsKillView(js, fo, "nope"); err == nil || !strings.Contains(err.Error(), "未找到任务/子代理") {
		t.Errorf("未知 id 应报错: %v", err)
	}
	// Kill 报错原样上抛(如「不在运行」)
	js.killErr = errString("任务不在运行")
	if _, err := jobsKillView(js, fo, "j2"); err == nil || !strings.Contains(err.Error(), "不在运行") {
		t.Errorf("Kill 错误应上抛: %v", err)
	}
}

func TestJobsIDOptions(t *testing.T) {
	js, fo := viewFixture()
	if got := jobsIDOptions(js, fo, []string{"/jobs", "list"}); got != nil {
		t.Errorf("list 无二级: %v", got)
	}
	if got := jobsIDOptions(js, fo, []string{"/jobs"}); got != nil {
		t.Errorf("未选子命令无二级: %v", got)
	}
	if got := jobsIDOptions(js, fo, []string{"/jobs", "output"}); len(got) != 6 {
		t.Errorf("output 应列全部 6 条: %v", got)
	}
	got := jobsIDOptions(js, fo, []string{"/jobs", "kill"})
	if len(got) != 2 {
		t.Fatalf("kill 只应列运行中(j2+a1): %v", got)
	}
	for _, o := range got {
		if o.Value != "j2" && o.Value != "a1" {
			t.Errorf("kill 候选取到非运行中项: %v", o)
		}
	}
	// 子代理带前缀便于区分
	for _, o := range got {
		if o.Value == "a1" && !strings.HasPrefix(o.Desc, "子代理") {
			t.Errorf("子代理候选应带前缀: %v", o)
		}
	}
	// 无 fanout 时退化为纯任务候选
	if got := jobsIDOptions(js, nil, []string{"/jobs", "kill"}); len(got) != 1 || got[0].Value != "j2" {
		t.Errorf("无 fanout 应只列任务: %v", got)
	}
}

func TestRowDurationAndHelpers(t *testing.T) {
	now := time.Now()
	if d := rowDuration(jobRow{state: "running", created: now.Add(-5 * time.Second)}); d == "-" {
		t.Errorf("运行中应给耗时: %q", d)
	}
	if d := rowDuration(jobRow{state: "done", created: now.Add(-90 * time.Second), done: now.Add(-30 * time.Second)}); d != "1m00s" {
		t.Errorf("已完成耗时 = %q, want 1m00s", d)
	}
	if d := rowDuration(jobRow{state: "done"}); d != "-" {
		t.Errorf("无创建时间应为 -: %q", d)
	}
	if d := rowDuration(jobRow{state: "done", created: now, done: now.Add(-time.Second)}); d != "-" {
		t.Errorf("结束早于创建应为 -: %q", d)
	}
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{2500 * time.Millisecond, "2.5s"},
		{68 * time.Second, "1m08s"},
		{3*time.Hour + 5*time.Minute, "3h05m"},
	} {
		if got := humanDur(c.d); got != c.want {
			t.Errorf("humanDur(%v) = %q, want %q", c.d, got, c.want)
		}
	}
	if got := clip("中文中文中文", 3); got != "中文中…" {
		t.Errorf("clip = %q", got)
	}
	if got := clip("a\nb", 10); got != "a b" {
		t.Errorf("换行应折叠: %q", got)
	}
	got := tailRunes("中文一二三四五", 3)
	if !strings.Contains(got, "三四五") || !strings.HasPrefix(got, "…(截断前 4 字符)") {
		t.Errorf("tail 截断错误: %q", got)
	}
}
