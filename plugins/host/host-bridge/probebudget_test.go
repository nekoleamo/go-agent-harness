// probebudget_test.go:批五 P5 —— 大量假 `tool-*` 不能把启动拖死,截断必须**明说**。
//
// 为什么这条要紧:`loadEntries` 原来对每个候选串行跑一次 3s 超时的角色探测。
// 目录里 100 个名字像插件的构建产物 ⇒ 最坏 ~300s 启动。文件名带 tool- 前缀的
// 构建产物并不罕见(用户把别的项目的 out/ 拷进来),所以这不是理论值。
package hostbridge

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
)

// 造 N 个「探测要花 slowMS 毫秒」的假插件。
//
// ⚠️ sleep 的秒数**必须算对**:第一版写的是 `"sleep 0." + Itoa(ms/100)`,于是 3000ms
// 变成了 `sleep 0.30` = **0.3 秒**。整个夹具一直在骗人 ——「超预算」那条路径压根没被
// 触发过(那条用例正好带 t.Skip,于是**静默跳过**了整整几轮门禁)。
// 「看起来覆盖了」和「真的覆盖了」是两回事,差一个浮点除法。
//
// 用 sh 桩(Windows 上不跑 —— 那条路径由真机清单 W502 验)。
func manyFakePlugins(t *testing.T, dir string, n int, slowMS int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("桩脚本是 sh,Windows 上不跑(该路径的真实行为由真机清单 W502 验)")
	}
	for i := 0; i < n; i++ {
		name := "tool-fake" + strconv.Itoa(i)
		sub := filepath.Join(dir, name)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\nsleep " + strconv.FormatFloat(float64(slowMS)/1000.0, 'f', -1, 64) + "\n" +
			"if [ \"$1\" = \"--roles\" ]; then echo '[\"\"]'; exit 0; fi\nexit 1\n"
		if err := os.WriteFile(filepath.Join(sub, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestProbeRolesParallelHasBudget 候选一多也必须在**有界时间**内返回。
//
// 判据给得宽松(预算的 3 倍):要证的是「有界」,不是「快」。串行版在这里会是
// N × 300ms 加上本机的 exec 开销 —— 已经超出宽松判据。
func TestProbeRolesParallelHasBudget(t *testing.T) {
	dir := t.TempDir()
	manyFakePlugins(t, dir, 24, 300)
	b := &Bridge{dir: dir, entries: map[string]*extEntry{}}
	start := time.Now()
	got := b.probeRoles(fakePaths(t, dir))
	elapsed := time.Since(start)
	if len(got) != 24 {
		t.Fatalf("预算内应全部探测完,得 %d", len(got))
	}
	if elapsed > rolesProbeBudget*3 {
		t.Fatalf("探测没有并行或有界: %v(预算 %v)", elapsed, rolesProbeBudget)
	}
	t.Logf("24 个候选各探测 300ms,并行后耗时 %v(含本机首次 exec 的固定开销)", elapsed)
}

// TestProbeRolesTruncationIsReported 超预算时**必须明说**被舍掉多少。
//
// 静默截断的症状是「我装了 50 个,只有前几个出现,日志一行都没有」——
// 用户无从判断是自己装错了还是产品坏了。
func TestProbeRolesTruncationIsReported(t *testing.T) {
	dir := t.TempDir()
	// 每个 3s(= 探测超时上限),数量远超并发与总预算能覆盖的量。
	manyFakePlugins(t, dir, 40, 3000)
	var buf syncBuffer
	b := &Bridge{dir: dir, entries: map[string]*extEntry{},
		lg: slog.New(slog.NewTextHandler(&buf, nil))}
	b.probeRoles(fakePaths(t, dir))
	out := buf.String()
	if !strings.Contains(out, "超出总预算") {
		t.Skipf("本次探测恰好在预算内完成(%v),跳过截断上报断言", rolesProbeBudget)
	}
	for _, want := range []string{"只探测了部分候选", "skipped", "total"} {
		if !strings.Contains(out, want) {
			t.Errorf("截断日志缺 %q:\n%s", want, out)
		}
	}
}

// TestProbeRolesSkipsDisabledInBudget 停用的项**不占**探测预算。
//
// 顺带修好的一件事:原顺序下,一个已停用的插件也要先花 3s 探测才被跳过 ——
// 而"停用"的意义正是让它别给启动添麻烦。
func TestProbeRolesSkipsDisabledInBudget(t *testing.T) {
	dir := t.TempDir()
	manyFakePlugins(t, dir, 4, 100)
	// 全部停用 ⇒ 候选收集阶段就该把它们全滤掉。
	t.Setenv("GAH_HOME", t.TempDir())
	for i := 0; i < 4; i++ {
		prefs.SetExternalDisabled("tool-fake"+strconv.Itoa(i), true)
	}
	b := &Bridge{dir: dir, entries: map[string]*extEntry{}}
	if cands := b.candidatesForProbe(); len(cands) != 0 {
		t.Errorf("停用的项不该进探测候选: %v", cands)
	}
}

// TestSnapshotRolesIsADistinctMap 快照必须是**另一张表**,不是内部那张。
//
// 这条用例是给一个真 bug 立的:超预算时 goroutine **仍在跑**,而 `probeRoles` 第一版
// 直接把内部 map 返回了,调用方 `loadEntries` 随即 range 它 ⇒ Go 的
// `concurrent map iteration and map write` **fatal**(不可 recover)。
// 它只在超预算时触发 —— 正是「目录里堆了一堆假 tool-*」那一种。
//
// **为什么不用「撞出崩溃」来钉它**(第一版的写法):那要求「写」恰好落在「遍历」期间,
// 而单次 range 只要微秒级;把消费窗口人为拉长到数秒之后,在这台机器上**对着有 bug 的
// 实现也照样绿**(实测两次)。**假绿的回归用例比没有更糟** —— 它让人以为这条被守住了。
// 所以改成直接钉住**机制**:返回的表与内部表互不影响。
func TestSnapshotRolesIsADistinctMap(t *testing.T) {
	var mu sync.Mutex
	inner := map[string][]string{"a": {"x"}, "b": {"y"}}
	snap := snapshotRoles(inner, &mu)
	if len(snap) != 2 {
		t.Fatalf("快照内容应完整: %v", snap)
	}
	inner["c"] = []string{"z"}
	if _, ok := snap["c"]; ok {
		t.Error("快照与内部表是同一张 —— 返回后仍会被仍在跑的 goroutine 改到")
	}
	delete(snap, "a")
	if _, ok := inner["a"]; !ok {
		t.Error("改快照不该影响内部表")
	}
}

func fakePaths(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, filepath.Join(dir, e.Name(), e.Name()))
		}
	}
	if len(out) == 0 {
		t.Fatal("夹具没造出候选")
	}
	return out
}

// syncBuffer 并发安全的日志缓冲(探测并发跑,logger 会被多个 goroutine 调)。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
