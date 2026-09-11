// 启动链单测:profile/bundle 加载、装配与启动、配置自愈回滚(生效树语义)、headless 输出。
// 锁定的是 boot 的真实不变量 —— 路径/配置解析的显式失败、自愈回滚后回传**生效**树、
// headless 输出模型最后一条 assistant 回复。
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/catalogue"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// errBoom 占位错误(验证 AgentLoop 错误原样上抛)。
var errBoom = errors.New("boom")

// —— 测试脚手架 ——

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testCtx(t *testing.T) *ctx.Ctx {
	t.Helper()
	logger := testLogger()
	return ctx.New(logger, event.New(logger))
}

// writeFile 写入测试夹具文件(自动建父目录)。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// profileDir 造一个最小可加载 profile 目录(profile-tui.yaml + bundle-base.yaml + patch)。
func profileDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bundle-base.yaml"), `name: base
entries:
  - id: host-session-log
  - id: host-llm
`)
	writeFile(t, filepath.Join(dir, "patch-note.yaml"), `entries:
  - id: host-llm
    enabled: false
`)
	writeFile(t, filepath.Join(dir, "profile-tui.yaml"), `name: tui
bundles:
  - base
patches:
  - patch-note.yaml
`)
	return dir
}

// captureStdout 捕获 fn 期间写向 os.Stdout 的输出(headless 输出断言)。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	out := <-done
	_ = r.Close()
	return out
}

// —— loadProfileTree ——

// TestLoadProfileTreeAppliesBundlesAndPatches 锁定:profile → bundle 条目按序应用,
// profile 自身 patch 覆盖同 id 条目(enabled:false 生效)。
func TestLoadProfileTreeAppliesBundlesAndPatches(t *testing.T) {
	dir := profileDir(t)
	tree, err := loadProfileTree(dir, "tui")
	if err != nil {
		t.Fatal(err)
	}
	if got := tree.List(); len(got) != 2 || got[0] != "host-session-log" || got[1] != "host-llm" {
		t.Fatalf("bundle 条目顺序不符: %v", got)
	}
	if !tree.Enabled("host-session-log") {
		t.Fatal("未显式关闭的条目应默认启用")
	}
	if tree.Enabled("host-llm") {
		t.Fatal("patch 的 enabled:false 应覆盖 bundle 默认(true)")
	}
	e, ok := tree.Get("host-llm")
	if !ok || e.Enabled == nil || *e.Enabled {
		t.Fatalf("patch 覆盖后条目应为显式关闭: %+v", e)
	}
}

// TestLoadProfileTreeExplicitErrors 锁定:缺 profile / 坏 profile YAML / 缺 bundle /
// 坏 patch 四种输入全都显式报错(不静默退化成空树)。
func TestLoadProfileTreeExplicitErrors(t *testing.T) {
	dir := profileDir(t)

	if _, err := loadProfileTree(dir, "nope"); err == nil {
		t.Fatal("profile 不存在应显式报错")
	}

	bad := t.TempDir()
	writeFile(t, filepath.Join(bad, "profile-tui.yaml"), "name: [unclosed\n")
	if _, err := loadProfileTree(bad, "tui"); err == nil {
		t.Fatal("坏 profile YAML 应显式报错")
	}

	missB := t.TempDir()
	writeFile(t, filepath.Join(missB, "profile-tui.yaml"), "name: tui\nbundles:\n  - ghost\n")
	if _, err := loadProfileTree(missB, "tui"); err == nil {
		t.Fatal("profile 引用的 bundle 文件缺失应显式报错")
	} else if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("错误信息应点名缺失 bundle: %v", err)
	}

	badPatch := t.TempDir()
	writeFile(t, filepath.Join(badPatch, "bundle-base.yaml"), "name: base\nentries:\n  - id: host-llm\n")
	writeFile(t, filepath.Join(badPatch, "profile-tui.yaml"), "name: tui\nbundles:\n  - base\npatches:\n  - p.yaml\n")
	writeFile(t, filepath.Join(badPatch, "p.yaml"), "entries: [oops\n")
	if _, err := loadProfileTree(badPatch, "tui"); err == nil {
		t.Fatal("坏 patch YAML 应显式报错")
	}
}

// —— enabledSet / catalogueInfo ——

// TestEnabledSetOnlyEnabledEntries 锁定:未声明条目(disabled=false / 不在树中)一律不启动,
// 且显式 enabled:false 过滤准确(装配只启动启用项)。
func TestEnabledSetOnlyEnabledEntries(t *testing.T) {
	off := false
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "on"},
		{ID: "off", Enabled: &off},
	})
	set := enabledSet(tree)
	if !set["on"] {
		t.Fatal("默认条目应在启动集内")
	}
	if set["off"] {
		t.Fatal("enabled:false 条目不应在启动集内")
	}
	if set["ghost"] {
		t.Fatal("未声明条目不应在启动集内")
	}
}

// TestCatalogueInfoCoversAllEntries 锁定:候选清单一览与 catalogue 单一事实源一致
// (id/type/bundle 齐全 —— host-plugin-manager 只读枚举依赖它)。
func TestCatalogueInfoCoversAllEntries(t *testing.T) {
	info := catalogueInfo()
	if len(info) != len(catalogue.All) {
		t.Fatalf("候选清单 %d 条与 catalogue %d 条不一致", len(info), len(catalogue.All))
	}
	for id, d := range catalogue.All {
		got, ok := info[id]
		if !ok {
			t.Fatalf("catalogue 条目 %s 未进入候选清单", id)
		}
		if got.ID != id || got.Type != d.Manifest.Type || got.Bundle != d.Bundle || got.Manage != d.Manage {
			t.Fatalf("候选清单条目 %s 与 catalogue 声明不一致: %+v", id, got)
		}
		if got.Type == "" {
			t.Fatalf("catalogue 条目 %s 缺 type(装配/管理域按 type 分派)", id)
		}
	}
}

// —— assembleAndStart / bootWithRecovery ——

// TestAssembleAndStartInjectsSystemServices 锁定:装配后 system.registry / system.catalogue
// 恒可用(插件管理器与外部桥经它们访问宿主内部服务)。
func TestAssembleAndStartInjectsSystemServices(t *testing.T) {
	reg, c, err := assembleAndStart(testLogger(), config.NewTree(), t.TempDir(), []string{"base"})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.DisposeAll()
	if reg.Snapshot() == nil {
		t.Fatal("空配置树应装配出注册表(未启动任何插件)")
	}
	var ops sdk.RegistryOps
	if err := c.Inject("system.registry", &ops); err != nil {
		t.Fatalf("system.registry 应可注入: %v", err)
	}
	var cat map[string]sdk.PluginInfo
	if err := c.Inject("system.catalogue", &cat); err != nil {
		t.Fatalf("system.catalogue 应可注入: %v", err)
	}
	if len(cat) != len(catalogue.All) {
		t.Fatalf("system.catalogue 应含全部候选插件: %d != %d", len(cat), len(catalogue.All))
	}
}

// TestAssembleAndStartUnknownBundleFails 锁定:配置声明了不存在的 bundle → 显式失败
// (不静默跳过,否则用户以为插件已启用)。
func TestAssembleAndStartUnknownBundleFails(t *testing.T) {
	_, _, err := assembleAndStart(testLogger(), config.NewTree(), t.TempDir(), []string{"ghost"})
	if err == nil {
		t.Fatal("未知 bundle 应显式报错")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("错误信息应点名未知 bundle: %v", err)
	}
}

// TestAssembleAndStartDuplicateBundleFails 锁定:同一 bundle 被重复声明/装配 → 显式失败
// (同名插件重复注册不得静默覆盖,否则装配结果取决于遍历顺序)。
func TestAssembleAndStartDuplicateBundleFails(t *testing.T) {
	_, _, err := assembleAndStart(testLogger(), config.NewTree(), t.TempDir(), []string{"base", "base"})
	if err == nil {
		t.Fatal("重复装配同一 bundle 应显式报错")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("错误信息应说明重复注册: %v", err)
	}
}

// TestIsStdinTTYDetection 锁定 TUI 启用前的非 TTY 判定:
// 字符设备(终端/dev/null)= TTY;普通文件/管道 = 非 TTY;无法探测时保守按非 TTY。
func TestIsStdinTTYDetection(t *testing.T) {
	old := os.Stdin
	defer func() { os.Stdin = old }()

	devNull, err := os.Open(os.DevNull) // 字符设备:mode&ModeCharDevice != 0
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	os.Stdin = devNull
	if !isStdinTTY() {
		t.Fatal("字符设备应判定为 TTY")
	}

	reg, err := os.Create(filepath.Join(t.TempDir(), "piped-input"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	os.Stdin = reg
	if isStdinTTY() {
		t.Fatal("普通文件(重定向输入)应判定为非 TTY")
	}

	closed, err := os.Create(filepath.Join(t.TempDir(), "closed"))
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	os.Stdin = closed // 已关闭:Stat 报错 → 保守非 TTY
	if isStdinTTY() {
		t.Fatal("无法探测 stdin 时应保守判定为非 TTY")
	}
}

// TestAssembleAndStartPluginFailureSurfaces 锁定:启用项启动失败 → 错误点名插件
// (本用例:只启用 host-agent-loop,其依赖 ctx.sessions 未装配 → 失败)。
func TestAssembleAndStartPluginFailureSurfaces(t *testing.T) {
	tree := config.NewTree()
	tree.Apply([]config.Entry{{ID: "host-agent-loop"}})
	_, _, err := assembleAndStart(testLogger(), tree, t.TempDir(), []string{"base"})
	if err == nil {
		t.Fatal("依赖缺失的启用项应显式报错")
	}
	if !strings.Contains(err.Error(), "host-agent-loop") {
		t.Fatalf("错误信息应点名失败插件: %v", err)
	}
}

// TestBootWithRecoverySuccessReturnsOriginalTree 锁定:首启成功路径回传**原始**树
// (不读备份、不改变语义),且绑定已启动的注册表/上下文。
func TestBootWithRecoverySuccessReturnsOriginalTree(t *testing.T) {
	tree := config.NewTree()
	reg, c, got, err := bootWithRecovery(testLogger(), tree, t.TempDir(), []string{"base"})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.DisposeAll()
	if got != tree {
		t.Fatal("成功路径应回传原始树")
	}
	if c == nil {
		t.Fatal("成功路径应回传上下文")
	}
}

// TestBootWithRecoveryRollsBackToEffectiveTree 锁定自愈不变量:
// 坏配置(启用项启动失败)→ 读最近备份 → 回滚重试成功 → 回传**备份树**;
// 且调用方把回传树写回备份时,坏配置不会覆盖唯一的好回滚点(否则自愈退化为一次性)。
func TestBootWithRecoveryRollsBackToEffectiveTree(t *testing.T) {
	dir := t.TempDir()
	// 先存一份"上次正常"的备份:空树(不启动任何插件)= 可启动
	if err := config.SaveBackup(config.NewTree(), dir, 10); err != nil {
		t.Fatal(err)
	}
	bad := config.NewTree()
	bad.Apply([]config.Entry{{ID: "host-agent-loop"}}) // 缺依赖 → 启动失败
	reg, _, got, err := bootWithRecovery(testLogger(), bad, dir, []string{"base"})
	if err != nil {
		t.Fatalf("有可回滚备份时应自愈成功: %v", err)
	}
	defer reg.DisposeAll()
	if got == bad || got.Enabled("host-agent-loop") {
		t.Fatal("回滚后必须回传生效(备份)树,而不是启动失败的树")
	}
	// 调用方语义(main.go):启动成功后备份**生效**树
	if err := config.SaveBackup(got, dir, 10); err != nil {
		t.Fatal(err)
	}
	back, err := config.LoadLatestBackup(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back == nil {
		t.Fatal("应有备份可读")
	}
	if back.Enabled("host-agent-loop") {
		t.Fatal("备份里出现了坏配置的启用项 → 下次自愈将回滚到坏配置(自愈退化)")
	}
}

// TestBootWithRecoveryExplicitFailures 锁定自愈的三条失败路径都显式报错并说明原因:
// 无备份可回滚 / 备份不可读 / 回滚后仍启动失败。
func TestBootWithRecoveryExplicitFailures(t *testing.T) {
	t.Run("无备份", func(t *testing.T) {
		_, _, _, err := bootWithRecovery(testLogger(), config.NewTree(), t.TempDir(), []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), "无备份可回滚") {
			t.Fatalf("无备份应显式说明: %v", err)
		}
	})
	t.Run("备份损坏", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, config.BackupDir, "config.latest.yaml"), "entries: [broken\n")
		_, _, _, err := bootWithRecovery(testLogger(), config.NewTree(), dir, []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), "回滚读取失败") {
			t.Fatalf("备份不可读应显式说明(不可当无备份): %v", err)
		}
	})
	t.Run("回滚后仍失败", func(t *testing.T) {
		dir := t.TempDir()
		if err := config.SaveBackup(config.NewTree(), dir, 10); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := bootWithRecovery(testLogger(), config.NewTree(), dir, []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), "ghost") {
			t.Fatalf("重试仍失败应回传原因: %v", err)
		}
	})
}

// —— runHeadless ——

type stubLoop struct {
	err   error
	input string
}

func (s *stubLoop) Run(_ context.Context, input string) error {
	s.input = input
	return s.err
}

// stubSessions 仅实现 DeriveMessages(其余方法由 nil 接口占位,本用例不触及)。
type stubSessions struct {
	sdk.SessionLog
	msgs []sdk.LLMMessage
}

func (s *stubSessions) DeriveMessages() []sdk.LLMMessage { return s.msgs }

// TestRunHeadlessMissingServices 锁定:headless 起跑前先校验 agentLoop / sessions,
// 缺失即显式报错(不静默输出空文本)。
func TestRunHeadlessMissingServices(t *testing.T) {
	c := testCtx(t)
	if err := runHeadless(c, "hi", testLogger()); err == nil {
		t.Fatal("缺 ctx.agentLoop 应显式报错")
	}
	loop := &stubLoop{}
	if err := c.Provide("ctx.agentLoop", sdk.AgentLoop(loop)); err != nil {
		t.Fatal(err)
	}
	if err := runHeadless(c, "hi", testLogger()); err == nil {
		t.Fatal("缺 ctx.sessions 应显式报错")
	}
	if loop.input != "hi" {
		t.Fatalf("应先跑完一轮再取会话: %q", loop.input)
	}
}

// TestRunHeadlessPrintsLastAssistant 锁定:输出模型最后一条 assistant 回复;
// 该轮无 assistant 消息时不输出任何内容;AgentLoop 报错原样上抛。
func TestRunHeadlessPrintsLastAssistant(t *testing.T) {
	c := testCtx(t)
	if err := c.Provide("ctx.agentLoop", sdk.AgentLoop(&stubLoop{})); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.sessions", sdk.SessionLog(&stubSessions{msgs: []sdk.LLMMessage{
		{Role: sdk.RoleUser, Content: "问"},
		{Role: sdk.RoleAssistant, Content: "中间回复"},
		{Role: sdk.RoleUser, Content: "再问"},
		{Role: sdk.RoleAssistant, Content: "最后回复"},
	}})); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := runHeadless(c, "问题", testLogger()); err != nil {
			t.Errorf("headless 应成功: %v", err)
		}
	})
	if out != "最后回复\n" {
		t.Fatalf("headless 应输出最后一条 assistant 回复,得 %q", out)
	}

	// 无 assistant 消息 → 不输出(不伪造内容)
	c2 := testCtx(t)
	if err := c2.Provide("ctx.agentLoop", sdk.AgentLoop(&stubLoop{})); err != nil {
		t.Fatal(err)
	}
	if err := c2.Provide("ctx.sessions", sdk.SessionLog(&stubSessions{msgs: []sdk.LLMMessage{
		{Role: sdk.RoleUser, Content: "只有提问"},
	}})); err != nil {
		t.Fatal(err)
	}
	if out := captureStdout(t, func() {
		if err := runHeadless(c2, "问题", testLogger()); err != nil {
			t.Errorf("headless 应成功: %v", err)
		}
	}); out != "" {
		t.Fatalf("无 assistant 消息时不应输出: %q", out)
	}

	// AgentLoop 报错 → 原样上抛(不吞错)
	c3 := testCtx(t)
	want := errBoom
	if err := c3.Provide("ctx.agentLoop", sdk.AgentLoop(&stubLoop{err: want})); err != nil {
		t.Fatal(err)
	}
	if err := runHeadless(c3, "问题", testLogger()); err != want {
		t.Fatalf("AgentLoop 错误应原样上抛,得 %v", err)
	}
}
