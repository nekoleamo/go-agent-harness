// 子进程覆盖率桥(2026-10-02):让**跑在子进程里的代码**也进覆盖率账。
//
// 问题:Go 的覆盖计数器由**进程退出时**写盘。测试为了跑 `main()` 或跑「施加
// Landlock 后 exec 自身」那类逻辑,必须起子进程 —— 子进程的计数器不写进父测试的
// profile,于是那部分代码在账上**永远是 0%**(与「这段代码没人跑」不可区分)。
// 受影响最大的三处:`cmd/gah` 的 main()、`internal/kernelsandbox` 与
// `plugins/tool/tool-shell` 的 Linux 自举 helper。
//
// 为什么以前不解决:当时把「子进程不写 cover 计数器」登记成**结构性盲区**,
// 棘轮按盲区取了保守值(tool-shell@linux 58 / kernelsandbox@linux 43 / cmd/gah 56)。
// 但 2026-10-02 新加的覆盖率**稀释门**靠读数判真假,盲区会直接让判读失真 ——
// 「这个包掉了 3pp」和「这段代码本来就测不到」必须能分开。
//
// 做法(三步,缺一不可):
//
//	① 子进程必须**带覆盖插桩**编译:`go build -cover`(测试里做,见 `BuildCovered`);
//	② 跑子进程时设 `GOCOVERDIR=<本次运行的目录>`:插桩过的程序把计数器写在那里;
//	③ 跑完用 `go tool covdata textfmt -i <目录> -o <profile>` 转成标准 profile,
//	   再由 `scripts/coverage-check.sh` 按「同一区块计数相加」并进主 profile。
//
// 边界(**别以为它能弥合一切**):
//   - 未设 `GAH_COVER_MERGE_DIR` 时本包**整体不启用**(返回空),测试照旧跑,
//     覆盖率门也就拿不到子进程那份 —— 缺数据与「测不到」必须能分开,而不是混成 0%。
//   - 只覆盖**同一次构建**插桩出来的子进程;插桩与否是测试自己的责任。
package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// MergeDirEnv 覆盖率合并目录的环境变量名(生成侧 = 覆盖率门,消费侧 = 测试)。
const MergeDirEnv = "GAH_COVER_MERGE_DIR"

// ChildSuffix 子进程 profile 的文件名后缀(同一目录可能被多个测试写,加序号区分)。
const ChildSuffix = ".child.out"

// ChildEnv 返回给子进程附加的环境变量(GOCOVERDIR=<本次运行目录>),无合并目录时返回 nil。
//
// 为什么要**每次运行一个子目录**:covdata 目录里是一组带序号的数据文件,多个
// 子进程写同一目录时 Go 会自动分配序号,理论可行;但一旦某个子进程异常退出留下
// 半截文件,后续 `covdata textfmt` 就会整体失败 —— 隔离到每次运行一个目录,
// 失败只影响那一次,且可以在 Flush 时单独跳过。
func ChildEnv(t *testing.T) []string {
	dir := RunDir(t)
	if dir == "" {
		return nil
	}
	return []string{"GOCOVERDIR=" + dir}
}

var (
	runMu   sync.Mutex
	runDir  string
	runSeq  int
	runOnce sync.Once
)

// RunDir 本次测试运行的子进程覆盖目录(全局唯一);未启用时返回 ""。
// t 允许为 nil(TestMain 场景没有 *testing.T)。
func RunDir(t *testing.T) string {
	root := os.Getenv(MergeDirEnv)
	if root == "" {
		return ""
	}
	runOnce.Do(func() {
		dir, err := os.MkdirTemp(root, "run-")
		if err != nil {
			runDir = "" // 拿不到目录就当没启用:宁可不合并,也不塞一个坏 profile 进覆盖率门
			return
		}
		runDir = dir
	})
	return runDir
}

// Flush 把本次运行的子进程覆盖数据转成 profile 并落到合并目录,由覆盖率门并入。
//
// **只保留 pkg 这一个包的块**:`go build -cover` 插桩的是**整个依赖树**(实测 cmd/gah
// 那个子进程产出 19640 个块,而父 profile 只有本包 216 个)。不筛的话合并会把
// core/plugins/internal 各包的测量也并进主 profile —— 分母被撑大、测量基准被混合,
// 那些包的棘轮读数从此不再是「它们自己测试的读数」。
//
// 为什么不在测试里直接合并:合并是**按区块计数相加**的全局动作,而每个测试只知道自己
// 那份数据 —— 让测试各写一份 profile、由门统一合并,顺序才不会因测试并行而变。
// 没有数据时**静默跳过**(该子进程可能根本没跑或没插桩);转换失败则显式报错 ——
// 静默吞掉会让「子进程覆盖本来就没接上」这件事永远没人发现。
//
// pkg 传**完整导入路径**(如 github.com/nekoleamo/go-agent-harness/cmd/gah)。
func Flush(t *testing.T, pkg string) {
	dir := RunDir(t)
	if dir == "" {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		failf(t, "读子进程覆盖目录失败: %v", err)
		return
	}
	if len(ents) == 0 {
		return // 没有数据 = 没插桩或没跑,不是错误
	}
	runMu.Lock()
	runSeq++
	seq := runSeq
	runMu.Unlock()

	root := os.Getenv(MergeDirEnv)
	tmp := filepath.Join(root, fmt.Sprintf("%s-%d.raw%s", filepath.Base(pkg), seq, ChildSuffix))
	cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i", dir, "-o", tmp)
	if b, err := cmd.CombinedOutput(); err != nil {
		failf(t, "covdata textfmt 失败(子进程覆盖没接上):%v\n%s", err, b)
		return
	}
	raw, err := os.ReadFile(tmp)
	os.Remove(tmp) // 中间文件不入合并目录(门只认 *.child.out)
	if err != nil {
		failf(t, "读回子进程 profile 失败: %v", err)
		return
	}
	out := filepath.Join(root, fmt.Sprintf("%s-%d%s", filepath.Base(pkg), seq, ChildSuffix))
	if err := os.WriteFile(out, filterProfile(raw, pkg), 0o644); err != nil {
		failf(t, "写子进程 profile 失败: %v", err)
	}
}

// filterProfile 只留下属于 pkg 的块,并补上 mode: 行(门与 go tool cover 都要求首行)。
func filterProfile(raw []byte, pkg string) []byte {
	prefix := pkg + "/"
	out := []byte("mode: atomic\n")
	for _, line := range splitLines(raw) {
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		if strings.HasPrefix(line, prefix) {
			out = append(out, line...)
			out = append(out, '\n')
		}
	}
	return out
}

func failf(t *testing.T, format string, args ...any) {
	if t != nil {
		t.Fatalf(format, args...)
		return
	}
	// TestMain 场景没有 *testing.T:只能打到 stderr 并以非零退出表达失败,
	// 否则「转换失败」会被静默吞掉 —— 那正是本桥要防的事。
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// BuildCovered 用覆盖插桩编译一个二进制(子进程要有计数器,必须带 -cover)。
//
// 为什么不直接 `go build`:不带 -cover 的产物**根本不写 GOCOVERDIR**,
// 设了环境变量也只是一次无效的期待 —— 这正是以前那个盲区的成因。
func BuildCovered(t *testing.T, out string, pkg string, extraArgs ...string) {
	t.Helper()
	args := append([]string{"build", "-cover", "-o", out}, extraArgs...)
	args = append(args, pkg)
	cmd := exec.Command("go", args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build -cover %s: %v\n%s", pkg, err, b)
	}
}

// MergeProfiles 把若干「同一次构建、同一区块」的 profile 并进 base(计数相加)。
//
// 放在这里而不是覆盖率门的 awk 里:合并逻辑需要按键聚合与求和,awk 写出来既难读又
// 没法单测;而覆盖率门只要在 awk 之前调一次它即可(脚本里已接)。
func MergeProfiles(base []byte, extra []byte) []byte {
	order := []string{}
	counts := map[string]int{}
	add := func(raw []byte) {
		for _, line := range splitLines(raw) {
			if line == "" {
				continue
			}
			key, n, ok := parseBlockLine(line)
			if !ok {
				continue
			}
			if _, seen := counts[key]; !seen {
				order = append(order, key)
			}
			counts[key] += n
		}
	}
	add(base)
	add(extra)
	out := make([]byte, 0, len(base)+len(extra))
	for _, k := range order {
		out = append(out, k...)
		out = append(out, ' ')
		out = append(out, fmt.Sprintf("%d", counts[k])...)
		out = append(out, '\n')
	}
	return out
}

// splitLines 按 '\n' 切行(空行保留给调用方过滤)。
func splitLines(raw []byte) []string {
	s := string(raw)
	if s == "" {
		return nil
	}
	lines := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// parseBlockLine 解析 profile 的一行 `文件:起.止 语句数 计数`。
// 键 = 「文件:区块 语句数」(语句数要进键 —— 同一区块在不同构建里语句数可能不同,
// 那时它们是**不同的块**,相加等于凭空造覆盖率)。
func parseBlockLine(line string) (key string, count int, ok bool) {
	// 从右往左切最后两段(语句数 / 计数),左边是位置。
	i := lastSpace(line)
	if i < 0 {
		return "", 0, false
	}
	countStr := line[i+1:]
	j := lastSpace(line[:i])
	if j < 0 {
		return "", 0, false
	}
	stmts := line[j+1 : i]
	if countStr == "" || stmts == "" {
		return "", 0, false
	}
	n, ok := allDigits(countStr)
	if !ok {
		return "", 0, false
	}
	// 语句数也必须是数字:它进键,非数字说明这一行不是 profile 行(合并时宁可丢掉,
	// 也不能把一个坏键并进覆盖率账)。
	if _, ok := allDigits(stmts); !ok {
		return "", 0, false
	}
	return line[:j+1] + stmts, n, true
}

// allDigits 解析「纯数字」字段(空串不算)。
func allDigits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func lastSpace(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ' ' {
			return i
		}
	}
	return -1
}

// resetRunDirForTest 只给单测用:包级的 sync.Once 让「同一进程里换一次合并目录」变难。
// 生产代码路径永远只跑一次(一次测试进程 = 一次运行),所以这个入口只在 _test.go 里用。
func resetRunDirForTest() {
	runMu.Lock()
	defer runMu.Unlock()
	runDir = ""
	runSeq = 0
	runOnce = sync.Once{}
}
