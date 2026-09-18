// worktrees_test.go:受管 worktree 的真实 git 行为(数据根落点/非 git 显式失败/回收语义)。
package hostworktrees

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-commands"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubSandbox 只提供 Root()(隔离测试不掺 policy-guard)。
type stubSandbox struct{ root string }

func (s *stubSandbox) Mode() sdk.SandboxMode     { return sdk.SandboxWorkspace }
func (s *stubSandbox) SetMode(sdk.SandboxMode)   {}
func (s *stubSandbox) Root() string              { return s.root }
func (s *stubSandbox) ValidatePath(string) error { return nil }
func (s *stubSandbox) ValidateRead(string) error { return nil }

// buildEnv 装配 host-worktrees(GAH_HOME = 临时数据根;工作区 = ws)。
func buildEnv(t *testing.T, ws string) sdk.Ctx {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if err := c.Provide("ctx.sandbox", &stubSandbox{root: ws}); err != nil {
		t.Fatal(err)
	}
	// 命令集:走真实注册表(与 TUI/Web 同一路径),顺带验证与 host-commands 的装配顺序无耦合
	if _, err := (&hostcommands.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	return c
}

// newRepo 建一个提交过的真实 git 仓库(无 git 则跳过)。
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("环境无 git")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	git(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "same.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func worktreesOf(t *testing.T, c sdk.Ctx) sdk.WorktreeService {
	t.Helper()
	var ws sdk.WorktreeService
	if err := c.Inject("ctx.worktrees", &ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

// TestCreateIsolatedWorktree 创建:落 $GAH_HOME/worktrees、有独立分支与完整检出、不改主工作区。
func TestCreateIsolatedWorktree(t *testing.T) {
	repo := newRepo(t)
	c := buildEnv(t, repo)
	ws := worktreesOf(t, c)

	wt, err := ws.Create(context.Background(), "任务A")
	if err != nil {
		t.Fatal(err)
	}
	if !within(sdk.Home(), wt.Path) {
		t.Fatalf("worktree 必须落在数据根内(便携纪律): %s vs %s", wt.Path, sdk.Home())
	}
	if within(repo, wt.Path) {
		t.Fatalf("worktree 不得落在用户仓库内: %s", wt.Path)
	}
	if fi, err := os.Stat(filepath.Join(wt.Path, "same.txt")); err != nil || fi.IsDir() {
		t.Fatalf("worktree 应是完整检出(tracked 文件在位): %v", err)
	}
	if wt.Branch != branchPrefix+wt.ID || wt.Base == "" {
		t.Fatalf("分支/基线不符: %+v", wt)
	}
	if got := git(t, repo, "rev-parse", "gah/"+wt.ID); got != wt.Base {
		t.Fatalf("隔离分支应指向创建时 HEAD: %s != %s", got, wt.Base)
	}
	// 主工作区干净:没有多出目录,也没有新分支被检出
	if out := git(t, repo, "status", "--porcelain"); out != "" {
		t.Fatalf("主工作区应保持干净: %q", out)
	}
	if out := git(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); out == wt.Branch {
		t.Fatalf("主工作区不应切到隔离分支: %s", out)
	}
}

// TestIsolationKeepsConcurrentWritesApart 验收核心:两个并行子代理写同名文件互不覆盖。
func TestIsolationKeepsConcurrentWritesApart(t *testing.T) {
	repo := newRepo(t)
	c := buildEnv(t, repo)
	ws := worktreesOf(t, c)

	// 模拟两个并发子代理:各自 worktree 内写同名文件
	type res struct {
		wt sdk.Worktree
	}
	var (
		out  = make([]res, 2)
		errs = make([]error, 2)
		done = make(chan int, 2)
	)
	for i := 0; i < 2; i++ {
		go func(i int) {
			wt, err := ws.Create(context.Background(), "并发")
			if err != nil {
				errs[i] = err
				done <- i
				return
			}
			out[i] = res{wt: wt}
			errs[i] = os.WriteFile(filepath.Join(wt.Path, "same.txt"), []byte("agent"+string(rune('A'+i))), 0o644)
			done <- i
		}(i)
	}
	for i := 0; i < 2; i++ {
		<-done
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("并发第 %d 路失败: %v", i, err)
		}
	}
	if out[0].wt.Path == out[1].wt.Path {
		t.Fatalf("两个子代理必须拿到不同 worktree: %s", out[0].wt.Path)
	}
	if got := readFile(t, filepath.Join(out[0].wt.Path, "same.txt")); got != "agentA" {
		t.Fatalf("worktree 0 内容被覆盖: %q", got)
	}
	if got := readFile(t, filepath.Join(out[1].wt.Path, "same.txt")); got != "agentB" {
		t.Fatalf("worktree 1 内容被覆盖: %q", got)
	}
	if got := readFile(t, filepath.Join(repo, "same.txt")); got != "base" {
		t.Fatalf("主工作区不应被隔离改动污染: %q", got)
	}
}

// TestCreateNonGitRepo 非 git 仓库:显式报错(不静默退化为非隔离运行)。
func TestCreateNonGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("环境无 git")
	}
	plain := t.TempDir()
	c := buildEnv(t, plain)
	_, err := worktreesOf(t, c).Create(context.Background(), "x")
	if err == nil {
		t.Fatal("非 git 仓库必须显式报错")
	}
	if !strings.Contains(err.Error(), "不是 git 仓库") {
		t.Fatalf("错误应说明原因: %v", err)
	}
	if !strings.Contains(err.Error(), plain) {
		t.Fatalf("错误应含工作目录(便于定位): %v", err)
	}
}

// TestCreateRepoWithoutCommit 无提交的仓库:显式报错(worktree 需要基线 commit)。
func TestCreateRepoWithoutCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("环境无 git")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	c := buildEnv(t, dir)
	_, err := worktreesOf(t, c).Create(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "HEAD") {
		t.Fatalf("无提交仓库应显式报错: %v", err)
	}
}

// TestListAndRemove List 以 git 记录为准(重启后仍在);Remove 回收目录、保留分支。
func TestListAndRemove(t *testing.T) {
	repo := newRepo(t)
	c := buildEnv(t, repo)
	ws := worktreesOf(t, c)
	wt, err := ws.Create(context.Background(), "回收")
	if err != nil {
		t.Fatal(err)
	}
	list := ws.List()
	if len(list) != 1 || list[0].ID != wt.ID || list[0].Path != wt.Path {
		t.Fatalf("List 应含刚创建的 worktree: %+v", list)
	}
	if err := ws.Remove(wt.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("回收后目录应消失: %v", err)
	}
	if got := git(t, repo, "rev-parse", "--verify", "refs/heads/"+wt.Branch); got == "" {
		t.Fatal("分支必须保留(未合并的改动不得静默丢弃)")
	}
	if len(ws.List()) != 0 {
		t.Fatal("回收后不应再列出")
	}
	if err := ws.Remove(wt.ID, false); err == nil {
		t.Fatal("回收不存在的 worktree 应报错")
	}
	// 未跟踪改动存在时:不带 force 拒绝(防静默丢活),带 force 放行
	wt2, err := ws.Create(context.Background(), "脏")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt2.Path, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ws.Remove(wt2.ID, false); err == nil {
		t.Fatal("有未跟踪改动时非 force 回收应失败")
	}
	if err := ws.Remove(wt2.ID, true); err != nil {
		t.Fatalf("force 回收应成功: %v", err)
	}
}

// TestCmdWorktree /worktree 命令:list/rm 文案与用法错误。
func TestCmdWorktree(t *testing.T) {
	repo := newRepo(t)
	c := buildEnv(t, repo)
	ws := worktreesOf(t, c)
	if out, err := wsCmd(t, c, "list"); err != nil || !strings.Contains(out, "无受管 worktree") {
		t.Fatalf("空列表文案: %q %v", out, err)
	}
	wt, err := ws.Create(context.Background(), "命令")
	if err != nil {
		t.Fatal(err)
	}
	out, err := wsCmd(t, c, "list")
	if err != nil || !strings.Contains(out, wt.ID) || !strings.Contains(out, wt.Path) {
		t.Fatalf("list 应含 id 与路径: %q %v", out, err)
	}
	if out, err = wsCmd(t, c, "rm", wt.ID); err != nil || !strings.Contains(out, "保留") {
		t.Fatalf("rm 应提示分支保留: %q %v", out, err)
	}
	if _, err := wsCmd(t, c, "rm"); err == nil {
		t.Fatal("缺 id 应报用法")
	}
	if _, err := wsCmd(t, c, "bogus"); err == nil {
		t.Fatal("未知子命令应报用法")
	}
	if _, err := wsCmd(t, c, "list"); err != nil {
		t.Fatal(err)
	}
}

// wsCmd 经注册表调用 /worktree(命令注册路径与 TUI 一致)。
func wsCmd(t *testing.T, c sdk.Ctx, args ...string) (string, error) {
	t.Helper()
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	if cmds == nil {
		t.Fatal("应注册 ctx.commands(host-commands 装配后)")
	}
	sp, ok := cmds.Get("worktree")
	if !ok {
		t.Fatal("应注册 /worktree 命令")
	}
	return sp.Run(args)
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}
