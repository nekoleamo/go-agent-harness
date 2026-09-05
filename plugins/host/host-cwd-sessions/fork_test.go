// P4-10 会话树/分支单测:ForkAt 从 seq 派生(继承事件/切新会话/命名)、CloneCurrent 复制全量、
// ForkPoints 抽取 user 分支点(坏行容忍)、接口断言。
package hostcwdsessions

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// forkSessions 带事件(可 Replay)的会话日志 fake。
type forkSessions struct {
	evs  []sdk.SessionEvent
	path string // 最近 Load 目标
}

func (f *forkSessions) Append(ev sdk.SessionEvent) error {
	f.evs = append(f.evs, ev)
	return nil
}
func (f *forkSessions) DeriveMessages() []sdk.LLMMessage { return nil }
func (f *forkSessions) Replay() []sdk.SessionEvent {
	return append([]sdk.SessionEvent(nil), f.evs...)
}
func (f *forkSessions) Flush() error                                  { return nil }
func (f *forkSessions) SetPath(string)                                {}
func (f *forkSessions) Load(p string) error                           { f.path = p; return nil }
func (f *forkSessions) SetHistory(int)                                {}
func (f *forkSessions) RegisterCompressor(int, sdk.SessionCompressor) {}

// histEvs 构造两轮会话事件(seq 1..4:user/assistant ×2)。
func histEvs() []sdk.SessionEvent {
	return []sdk.SessionEvent{
		{Seq: 1, Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "第一问"}},
		{Seq: 2, Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "第一答"}},
		{Seq: 3, Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "第二问"}},
		{Seq: 4, Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "第二答"}},
	}
}

// forkSetup GAH_HOME 隔离 + 事件型服务(当前会话指向会话目录)。
func forkSetup(t *testing.T) (*Service, *forkSessions) {
	t.Helper()
	withGahHome(t, t.TempDir())
	fs := &forkSessions{}
	evs := histEvs()
	for _, ev := range evs {
		if err := fs.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	// 模拟 sessionlog 已把历史落盘到主会话文件(ForkPoints 读盘、ForkAt 继承内存事件)
	if err := writeEvents(SessionPath(SessionsRoot(), "k", ""), evs); err != nil {
		t.Fatal(err)
	}
	svc := &Service{key: "k", sessions: fs}
	return svc, fs
}

// readLines 读文件行(坏行原样保留计数)。
func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) > 0 {
			out = append(out, sc.Text())
		}
	}
	return out
}

func TestForkAtInheritsToSeq(t *testing.T) {
	svc, fs := forkSetup(t)
	id, err := svc.ForkAt(3) // 继承到“第二问”(不继承第二答)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("fork 应返回新会话 id")
	}
	// 新文件写到会话目录且含 3 条继承事件
	path := SessionPath(SessionsRoot(), "k", id)
	if !fileExists(path) {
		t.Fatalf("分支文件应存在: %s", path)
	}
	lines := readLines(t, path)
	if len(lines) != 3 {
		t.Fatalf("应继承 3 条事件(seq≤3),got %d", len(lines))
	}
	// 切到新会话(后续续记只落新文件)
	if svc.CurrentSession() != id || !strings.HasSuffix(fs.path, "k-"+id+".jsonl") {
		t.Fatalf("fork 应切换并 Load 新文件: current=%q loaded=%q", svc.CurrentSession(), fs.path)
	}
	// 显示名标记 fork 来源
	if got := svc.SessionName(); !strings.Contains(got, "fork@3") {
		t.Fatalf("分支应自动命名标记: %q", got)
	}
	// 无继承(seq 低于全部事件)显式报错
	if _, err := svc.ForkAt(0); err == nil {
		t.Fatal("无可继承事件的 fork 应报错")
	}
}

func TestForkPointsExtract(t *testing.T) {
	svc, _ := forkSetup(t)
	// 主会话文件(空 id)的 user 消息:seq 1/3
	c, err := svc.ForkPoints("")
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 2 || c[0].Seq != 1 || c[1].Seq != 3 {
		t.Fatalf("主会话分支点: %+v", c)
	}
	id, _ := svc.ForkAt(3)
	c2, err := svc.ForkPoints(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(c2) != 2 || c2[1].Seq != 3 {
		t.Fatalf("分支会话分支点: %+v", c2)
	}
	// 不存在会话 = 空(不报错)
	if c3, err := svc.ForkPoints("ghost-id"); err != nil || len(c3) != 0 {
		t.Fatalf("不存在会话应空: %+v %v", c3, err)
	}
}

func TestForkPointsBadLineTolerated(t *testing.T) {
	svc, _ := forkSetup(t)
	// 在主会话文件头部插入坏行
	main := SessionPath(SessionsRoot(), "k", "")
	if err := os.WriteFile(main, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 再 append 真实事件行
	svc.sessions.SetPath(main)
	for _, ev := range histEvs() {
		b, _ := json.Marshal(ev)
		f, _ := os.OpenFile(main, os.O_APPEND|os.O_WRONLY, 0o600)
		f.Write(append(b, '\n'))
		f.Close()
	}
	c, err := svc.ForkPoints("")
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 2 {
		t.Fatalf("坏行应容忍,分支点仍 2: %+v", c)
	}
}

func TestCloneCurrentCopiesAll(t *testing.T) {
	svc, fs := forkSetup(t)
	id, err := svc.CloneCurrent()
	if err != nil {
		t.Fatal(err)
	}
	path := SessionPath(SessionsRoot(), "k", id)
	lines := readLines(t, path)
	if len(lines) != 4 {
		t.Fatalf("clone 应复制全量 4 条,got %d", len(lines))
	}
	if svc.CurrentSession() != id || !strings.HasSuffix(fs.path, "k-"+id+".jsonl") {
		t.Fatalf("clone 应切新会话: %q %q", svc.CurrentSession(), fs.path)
	}
}

func TestForkableSessionsAssertion(t *testing.T) {
	svc := &Service{sessions: &forkSessions{}}
	var cs sdk.CwdSessions = svc
	if _, ok := cs.(sdk.ForkableSessions); !ok {
		t.Fatal("Service 应实现 sdk.ForkableSessions(/fork 依赖)")
	}
}
