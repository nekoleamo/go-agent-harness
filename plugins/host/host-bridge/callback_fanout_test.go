// callback_fanout_test.go M9.3:sdk 回调协议 send/fork 分支分发与 cbFanout 代理 RPC 转发。
package hostbridge

import (
	"context"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubFanoutForBridge 仅 fanout 协议测试用的全接口替身。
type stubFanoutForBridge struct {
	sentID  string
	sentMsg string
	forkIn  string
}

func (s *stubFanoutForBridge) Agent(context.Context, string) (string, error) { return "", nil }
func (s *stubFanoutForBridge) Parallel(context.Context, []string) []sdk.FanoutResult {
	return nil
}
func (s *stubFanoutForBridge) Pipeline(context.Context, []string) ([]sdk.FanoutResult, string, error) {
	return nil, "", nil
}
func (s *stubFanoutForBridge) SpawnAgent(context.Context, string) (string, error) { return "ag1", nil }
func (s *stubFanoutForBridge) Fork(_ context.Context, input string) (string, error) {
	s.forkIn = input
	return "ag10", nil
}
func (s *stubFanoutForBridge) SendMessage(id, msg string) error {
	if id == "bad" {
		return errInjectionFailed
	}
	s.sentID, s.sentMsg = id, msg
	return nil
}
func (s *stubFanoutForBridge) ListAgents() []sdk.AgentHandle { return nil }
func (s *stubFanoutForBridge) AgentStatus(string) (sdk.AgentHandle, bool) {
	return sdk.AgentHandle{}, false
}
func (s *stubFanoutForBridge) KillAgent(string) error { return nil }

type errInjectionFailedT string

var errInjectionFailed = errInjectionFailedT("注入失败")

func (e errInjectionFailedT) Error() string { return string(e) }

var _ sdk.FanoutService = (*stubFanoutForBridge)(nil)

// TestCallbackFanoutSendForkDispatch fanoutCall send/fork 分支分发。
func TestCallbackFanoutSendForkDispatch(t *testing.T) {
	st := &stubFanoutForBridge{}
	cb := NewCallback(nil, nil, st, "")

	// send:注入消息透传,reply 空
	var reply string
	if err := cb.fanoutCall(context.Background(), "send", `{"ID":"ag1","Message":"补充要求"}`, &reply); err != nil {
		t.Fatal(err)
	}
	if reply != "" || st.sentID != "ag1" || st.sentMsg != "补充要求" {
		t.Fatalf("send 应透传注入: reply=%q sent=%s/%s", reply, st.sentID, st.sentMsg)
	}
	// send 业务失败:错误经 reply 回传(Go 错误 nil)
	reply = ""
	if err := cb.fanoutCall(context.Background(), "send", `{"ID":"bad","Message":"x"}`, &reply); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "注入失败") {
		t.Fatalf("send 业务失败应经 reply 回传: %q", reply)
	}
	// fork:input 透传,reply 为句柄 id
	reply = ""
	if err := cb.fanoutCall(context.Background(), "fork", `{"Input":"父上下文任务"}`, &reply); err != nil {
		t.Fatal(err)
	}
	if reply != "ag10" || st.forkIn != "父上下文任务" {
		t.Fatalf("fork 应返回句柄: reply=%q forkIn=%q", reply, st.forkIn)
	}
	// 未知方法仍显式报错
	if err := cb.fanoutCall(context.Background(), "jump", "", &reply); err == nil {
		t.Fatal("未知 fanout 方法应报错")
	}
}

// TestCbFanoutProxySendFork 外部代理:send/fork 经 RPC 转发宿主并回传结果/错误。
func TestCbFanoutProxySendFork(t *testing.T) {
	st := &stubFanoutForBridge{}
	cb := NewCallback(nil, nil, st, "tok")
	addr, close, err := serveCallback(cb)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	t.Setenv("GAH_CB_TOKEN", "tok")
	cc, err := DialCallback(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()

	f := CbFanout(cc)
	// SendMessage RPC 转发 → stub 收到
	if err := f.SendMessage("ag1", "你好子代理"); err != nil {
		t.Fatal(err)
	}
	if st.sentID != "ag1" || st.sentMsg != "你好子代理" {
		t.Fatalf("stub 应收到注入: %s/%s", st.sentID, st.sentMsg)
	}
	// 业务失败经代理回传为 Go 错误
	if err := f.SendMessage("bad", "x"); err == nil || !strings.Contains(err.Error(), "注入失败") {
		t.Fatalf("业务失败应回传错误: %v", err)
	}
	// Fork RPC 转发 → 句柄回传
	id, err := f.Fork(context.Background(), "继承父上下文")
	if err != nil {
		t.Fatal(err)
	}
	if id != "ag10" || st.forkIn != "继承父上下文" {
		t.Fatalf("fork 应回传句柄: id=%q forkIn=%q", id, st.forkIn)
	}
}

// stubJobsNilErr Kill 成功返回 nil error(host-jobs 契约);回调层曾直接 .Error()
// 解引用 → net/rpc handler panic 崩宿主。
type stubJobsNilErr struct{ killed []string }

func (s *stubJobsNilErr) Submit(string) (string, error)   { return "", nil }
func (s *stubJobsNilErr) Run(sdk.JobFunc) (string, error) { return "", nil }
func (s *stubJobsNilErr) List() []sdk.Job                 { return nil }
func (s *stubJobsNilErr) Kill(id string) error {
	s.killed = append(s.killed, id)
	return nil
}
func (s *stubJobsNilErr) Output(string) (sdk.Job, bool) { return sdk.Job{}, false }

// TestCallbackJobsKillNilError jobs.kill 成功路径(nil error)必须正常返回,不 panic。
func TestCallbackJobsKillNilError(t *testing.T) {
	cb := NewCallback(nil, &stubJobsNilErr{}, nil, "")
	var reply string
	err := cb.Call(CallArgs{Service: "jobs", Method: "kill", Args: `{"ID":"j1"}`}, &reply)
	if err != nil {
		t.Fatalf("回调不应报错: %v", err)
	}
	if reply != "" {
		t.Fatalf("成功应返回空字符串,得 %q", reply)
	}
}
