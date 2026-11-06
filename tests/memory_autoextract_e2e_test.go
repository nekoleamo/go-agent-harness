// 自动生成记忆的端到端(P1):回合结束后,规则闸命中 → 后台提取 → 白名单直写落盘。
//
// 为什么必须端到端:插件单测证明的是"给定 llm 返回值时的分流",证明不了**整条链通着** ——
// 中间还隔着 agent-loop 的 observeTurn 投递、ctx 的 Inject、以及异步 worker 的收尾。
//
// 三段互为证伪面:
//
//	① 命中规则闸的一句话 ⇒ 记忆文件里出现 mock 提取的那条(且带来源与 kind);
//	② 不命中规则闸的一句话 ⇒ 记忆文件**不变**、模型请求次数不增(成本闸门);
//	③ 提取是异步的 ⇒ 回合返回时文件可能还没写完,所以用例等它(而不是假设同步)。
package tests

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/internal/memory"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildExtractEnv 装配真实 llm-mock + host-memory(自动提取开)。
func buildExtractEnv(t *testing.T) sdk.Ctx {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "llm-mock"},
		{ID: "tool-shell"},
		{ID: "host-memory", Data: map[string]any{"extract": "rule"}},
		{ID: "host-agent-loop"},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c
}

func readUserMemory(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(memory.UserPath())
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func waitMemoryContains(t *testing.T, want string, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if raw := readUserMemory(t); strings.Contains(raw, want) {
			return raw
		}
		time.Sleep(20 * time.Millisecond)
	}
	return readUserMemory(t)
}

func TestMemoryAutoExtractEndToEnd(t *testing.T) {
	c := buildExtractEnv(t)
	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}

	// ② 基线:不命中规则闸的一句话,不该产生任何记忆。
	if err := loop.Run(context.Background(), "帮我看看这个函数在哪"); err != nil {
		t.Fatalf("基线回合: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // 给 worker 一个"万一它乱跑"的机会
	if raw := readUserMemory(t); strings.Contains(raw, "gate.sh") {
		t.Fatalf("规则未命中却写了记忆:\n%s", raw)
	}

	// ① 命中规则闸:提取结果(mock 固定返回一条 convention,conf 0.9)应直写落盘。
	if err := loop.Run(context.Background(), "以后都先跑 gate.sh 再提交"); err != nil {
		t.Fatalf("命中回合: %v", err)
	}
	raw := waitMemoryContains(t, "gate.sh", 6*time.Second)
	if !strings.Contains(raw, "gate.sh") {
		// 失败时把提取观测一起打出来:否则只能猜"是没触发、还是慢"。
		var es sdk.MemoryExtractStats
		_ = c.Inject("ctx.memory", &es)
		t.Fatalf("自动提取的记忆应落盘(带来源可追溯);最近一次提取=%+v\n%s", es.LastExtract(), raw)
	}
	if !strings.Contains(raw, "kind=convention") {
		t.Fatalf("直写条目应带 kind 元数据(否则检索层与治理面看不到类型):\n%s", raw)
	}
	if !strings.Contains(raw, "来源: 会话") {
		t.Fatalf("直写条目应带来源会话(按来源整段删的前提):\n%s", raw)
	}

	// ③ 观测面:本会话最近一次提取应有产出计数(而不是只有"成功了"三个字)。
	//
	// 这里必须**轮询**而不是紧跟文件断言:提取是异步的,实现顺序是「先落盘、再记观测」——
	// 文件一出现就读 LastExtract 会在慢机器上读到**上一轮**(规则未命中)的观测值。
	// Windows CI 就这么红过一次(run 42168857369),本地 macOS 快得多所以从没复现。
	var es sdk.MemoryExtractStats
	if err := c.Inject("ctx.memory", &es); err != nil || es == nil {
		t.Fatalf("ctx.memory 应实现 MemoryExtractStats: %v", err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && es.LastExtract().Produced == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	snap := es.LastExtract()
	if snap.Produced == 0 || snap.Written == 0 {
		t.Fatalf("观测值应记产出与直写条数(等不到有产出的观测),got %+v", snap)
	}
	if !es.ExtractEnabled() {
		t.Fatal("data.extract=rule 时 ExtractEnabled 应为 true")
	}
}
