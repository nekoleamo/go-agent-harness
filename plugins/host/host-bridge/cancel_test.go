// 执行可中断(⑥)测试:
//   - serve 层直调:CallID 登记/取消/清理语义,TimeoutMs 传导;
//   - 真实外部进程:宿主 ctx 取消 → Plugin.Cancel RPC → 插件侧 ctx 真的被中断
//     (以插件写的标记文件为证,而非只看宿主是否提前返回);
//   - 超时路径:插件按宿主下发的 TimeoutMs(80%)自行结束。
package hostbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// ctxTool 观察 ctx 的工具:记录 started/cancelled 标记,便于断言"插件侧真的被中断"。
// body 模板供临时插件与进程内直调共用。
func ctxToolBody() string {
	return `package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	hostbridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

type ctxTool struct{ timeoutMs int64 }

func (t ctxTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "slow", Description: "观察 ctx 的慢工具", TimeoutMs: t.timeoutMs,
		InputSchema: map[string]any{"type": "object"}}
}

func (t ctxTool) Execute(ctx context.Context, raw string) (any, error) {
	var a struct {
		Dir string ` + "`json:\"dir\"`" + `
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, err
	}
	_ = os.WriteFile(filepath.Join(a.Dir, "started"), []byte("1"), 0o644)
	select {
	case <-ctx.Done():
		_ = os.WriteFile(filepath.Join(a.Dir, "cancelled"), []byte(ctx.Err().Error()), 0o644)
		return nil, fmt.Errorf("工具被中断: %w", ctx.Err())
	case <-time.After(30 * time.Second):
		_ = os.WriteFile(filepath.Join(a.Dir, "finished"), []byte("1"), 0o644)
		return "未被打断", nil
	}
}

func main() {
	hostbridge.ServeTools(map[string]sdk.Tool{"slow": ctxTool{}}, nil)
}`
}

func waitFile(t *testing.T, path string, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestToolServerCancelSemantics serve 层直调:Cancel 命中运行中的调用并中断工具 ctx,
// 未命中(未知/已结束)返回 false 且不报错(取消是尽力而为)。
func TestToolServerCancelSemantics(t *testing.T) {
	dir := t.TempDir()
	srv := &toolServer{tools: map[string]sdk.Tool{"slow": ctxToolLocal{dir: dir}}, running: map[string]context.CancelFunc{}}

	// 未知 CallID:false 且无错
	var ok bool
	if err := srv.Cancel(&CancelArgs{CallID: "nope"}, &ok); err != nil || ok {
		t.Fatalf("未命中应返回 false 且无错: ok=%v err=%v", ok, err)
	}

	reply := &ExecReply{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.ExecuteNamed(&ExecNamedArgs{Name: "slow", JSONArgs: fmt.Sprintf(`{"dir":%q}`, dir), CallID: "c1"}, reply)
	}()
	if !waitFile(t, filepath.Join(dir, "started"), 5*time.Second) {
		t.Fatal("工具未开始执行")
	}
	if err := srv.Cancel(&CancelArgs{CallID: "c1"}, &ok); err != nil || !ok {
		t.Fatalf("Cancel 应命中: ok=%v err=%v", ok, err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后调用未及时返回")
	}
	if !strings.Contains(reply.Error, "context canceled") {
		t.Fatalf("reply.Error 应含取消语义: %q", reply.Error)
	}
	// 已结束:登记项清理,再次 Cancel 返回 false(不泄漏 running 表项)
	if err := srv.Cancel(&CancelArgs{CallID: "c1"}, &ok); err != nil || ok {
		t.Fatalf("已结束的调用应返回 false: ok=%v err=%v", ok, err)
	}
}

// TestToolServerTimeoutPropagates TimeoutMs 下发到插件侧 ctx(插件自行结束)。
func TestToolServerTimeoutPropagates(t *testing.T) {
	dir := t.TempDir()
	srv := &toolServer{tools: map[string]sdk.Tool{"slow": ctxToolLocal{dir: dir}}, running: map[string]context.CancelFunc{}}
	reply := &ExecReply{}
	start := time.Now()
	if err := srv.ExecuteNamed(&ExecNamedArgs{Name: "slow", JSONArgs: fmt.Sprintf(`{"dir":%q}`, dir), CallID: "t1", TimeoutMs: 150}, reply); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("超时应在插件侧生效并快速返回,实际 %s", d)
	}
	if !strings.Contains(reply.Error, "deadline exceeded") {
		t.Fatalf("reply.Error 应含超时语义: %q", reply.Error)
	}
}

// TestExternalToolCancelInterruptsProcess 全链路:宿主取消 ctx → Cancel RPC → 插件进程内工具
// 立即中断(标记文件为证)。插件声明 TimeoutMs=30s(远超断言窗口)——排除了"宿主超时"
// 这一替代解释,唯一可能的路径就是协议级 Cancel。
func TestExternalToolCancelInterruptsProcess(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(ctxToolBody(), "ctxTool{}", "ctxTool{timeoutMs: 30000}", 1)
	buildTempCmdPlugin(t, dir, "tool-slow", body)
	c, _ := buildEnv(t, dir)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	args, _ := json.Marshal(map[string]any{"dir": work})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan *sdk.ToolResult, 1)
	go func() {
		res, _ := tools.Execute(ctx, "slow", string(args))
		done <- res
	}()
	if !waitFile(t, filepath.Join(work, "started"), 10*time.Second) {
		t.Fatal("外部工具未开始执行(插件加载失败?)")
	}
	cancel()
	select {
	case res := <-done:
		if res == nil || !strings.Contains(res.Error, "取消") {
			t.Fatalf("取消应回结构化错误: %+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后宿主调用未返回")
	}
	if !waitFile(t, filepath.Join(work, "cancelled"), 10*time.Second) {
		t.Fatal("插件侧执行未被真正中断(协议级 cancel 未生效)")
	}
	if _, err := os.Stat(filepath.Join(work, "finished")); err == nil {
		t.Fatal("工具不应跑到自然结束")
	}
}

// ctxToolLocal 进程内版本(serve 层直调用;与 ctxToolBody 的临时插件同语义)。
type ctxToolLocal struct{ dir string }

func (t ctxToolLocal) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "slow", Description: "观察 ctx 的慢工具", InputSchema: map[string]any{"type": "object"}}
}

func (t ctxToolLocal) Execute(ctx context.Context, raw string) (any, error) {
	var a struct {
		Dir string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, err
	}
	_ = os.WriteFile(filepath.Join(a.Dir, "started"), []byte("1"), 0o644)
	select {
	case <-ctx.Done():
		_ = os.WriteFile(filepath.Join(a.Dir, "cancelled"), []byte(ctx.Err().Error()), 0o644)
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		_ = os.WriteFile(filepath.Join(a.Dir, "finished"), []byte("1"), 0o644)
		return "未被打断", nil
	}
}
