// Package hostworktrees 提供 host-worktrees 插件(S-P1-4):ctx.worktrees 受管 git worktree 服务。
//
// 解决的问题:并行子代理/任务在同一份工作区上互相覆盖文件(同名文件后写者胜)。隔离使每个
// 执行者拿到**独立工作目录 + 独立分支**,改动不落主工作区,由发起方显式决定合并/丢弃。
//
// 三条硬约束:
//  1. **不进用户仓库**:worktree 落 `$GAH_HOME/worktrees/<仓库名>-<id>`(便携纪律:gah 产生的
//     数据一律在数据根内);不改 .gitignore / .git/info/exclude,不动主工作区文件。
//  2. **非 git 显式失败**:不是 git 仓库 / 无 git 可执行文件 / 仓库无提交 → 报错并说明,绝不静默
//     退化成非隔离运行(用户以为隔离了而实际没有,比不支持更糟)。
//  3. **回收显式**:删除 worktree 会丢未提交改动,故默认只提示路径;回收必须显式 `/worktree rm`
//     (或 sdk.WorktreeService.Remove);分支**保留**(未合并的改动仍在分支上,不静默丢弃)。
//
// git 操作用互斥锁串行化:`git worktree add` 并发写同一 .git 目录会争锁/半成品。
package hostworktrees

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-worktrees。requires 无(沙箱/命令表可选注入)。
type Plugin struct{}

// Name 返回插件 id。
func (p *Plugin) Name() string { return "host-worktrees" }

// Start 提供 ctx.worktrees,并在装配了 ctx.commands 时注册 /worktree。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	w := &Worktrees{c: c, made: map[string]sdk.Worktree{}}
	if err := c.Provide("ctx.worktrees", w); err != nil {
		return nil, err
	}
	// 命令注册(可选注入:未装配 ctx.commands / 无 UI 时跳过,不报错 —— 与 host-jobs 同模式)
	var cmds sdk.CommandRegistry
	_ = c.Inject("ctx.commands", &cmds)
	if cmds == nil {
		return func() {}, nil
	}
	d, err := cmds.Register(sdk.CommandSpec{
		Name:  "worktree",
		Usage: "/worktree list|rm <id> [force]",
		Desc:  "受管 git worktree(隔离子代理)",
		Run:   w.cmdWorktree,
		// 交互式选择器级联:一级 list/rm;二级动态枚举受管 worktree ID(rm 时)
		Args: []sdk.ArgLevel{
			{Options: func([]string) []sdk.Option {
				return []sdk.Option{
					{Value: "list", Desc: "列出受管 worktree(路径/分支/基线)"},
					{Value: "rm", Desc: "回收 worktree(分支保留)"},
				}
			}},
			{Options: func(picked []string) []sdk.Option { return w.idOptions(picked) }},
		},
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// Worktrees 实现 sdk.WorktreeService(git 子进程驱动,零第三方依赖)。
type Worktrees struct {
	c sdk.Ctx // 惰性取 ctx.sandbox(可能后于本插件启动)

	mu   sync.Mutex // 串行化 git worktree 操作(并发 add/remove 会争 .git 锁)
	seq  int
	made map[string]sdk.Worktree // 本进程创建的记录(补 CreatedAt/Base 展示信息)
}

// 目录名惯例:`<仓库名>-<id>`;分支 `gah/<id>`。id 取目录名(全局唯一、可直接回填命令)。
const (
	branchPrefix = "gah/"
	dirPerm      = 0o755
	// maxIDProbe 目录名冲突时的最大探测次数(残留同名目录 → 换下一个 id)。
	maxIDProbe = 1000
	// maxNameLen 目录名里标签部分的长度上限(保留 -wtN 后缀的余地,防超长名字拷屏难读)。
	maxNameLen = 24
)

// baseDir 受管 worktree 的根(便携纪律:数据根派生,不落用户仓库、不落系统目录)。
func baseDir() string { return filepath.Join(sdk.Home(), "worktrees") }

// workspaceDir 当前工作区目录(沙箱 root 优先;未装配退回进程 cwd = gah 启动目录)。
func (w *Worktrees) workspaceDir() (string, error) {
	var sb sdk.Sandbox
	if w.c != nil {
		_ = w.c.Inject("ctx.sandbox", &sb)
	}
	if sb != nil && strings.TrimSpace(sb.Root()) != "" {
		return sb.Root(), nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("host-worktrees: 取当前工作目录失败: %w", err)
	}
	return wd, nil
}

// Create 新建受管 worktree:基于当前 HEAD 新建分支 + 检出到 $GAH_HOME/worktrees/<仓库名>-<id>。
func (w *Worktrees) Create(_ context.Context, label string) (sdk.Worktree, error) {
	dir, err := w.workspaceDir()
	if err != nil {
		return sdk.Worktree{}, err
	}
	repo, err := w.repoRoot(dir)
	if err != nil {
		return sdk.Worktree{}, err
	}
	base, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		return sdk.Worktree{}, fmt.Errorf(
			"host-worktrees: 取 HEAD 失败(仓库可能还没有提交,worktree 隔离需要至少一个提交): %w", err)
	}
	root := baseDir()
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return sdk.Worktree{}, fmt.Errorf("host-worktrees: 建数据目录失败(%s): %w", root, err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	// id == 目录名(单一事实源:git 记录里只有路径,列表/回收都以目录名定位)
	name := sanitizeName(filepath.Base(repo))
	if l := sanitizeName(label); l != "" {
		name = l // 自带标签(如任务名)优先:列表里一眼可辨认用途
	}
	if len(name) > maxNameLen {
		name = strings.Trim(name[:maxNameLen], "-_")
	}
	var id, path string
	for i := 0; i < maxIDProbe; i++ {
		w.seq++
		id = fmt.Sprintf("%s-wt%d", name, w.seq)
		path = filepath.Join(root, id)
		if _, serr := os.Stat(path); os.IsNotExist(serr) {
			break
		}
		if i == maxIDProbe-1 {
			return sdk.Worktree{}, fmt.Errorf("host-worktrees: %s 下同名目录过多,无法分配新 worktree", root)
		}
	}
	branch := branchPrefix + id
	// 路径 realpath 归一(与 git 记录一致:macOS 上 /tmp 是 /private/tmp 软链,不归一化会出现
	// “创建时是 /tmp/...、列表/回收时是 /private/tmp/...”两个事实源)
	path = realPath(path)
	if _, err := runGit(repo, "worktree", "add", "-b", branch, path, base); err != nil {
		return sdk.Worktree{}, fmt.Errorf("host-worktrees: 创建 worktree 失败: %w", err)
	}
	wt := sdk.Worktree{ID: id, Path: path, Repo: repo, Branch: branch, Base: base, CreatedAt: time.Now()}
	w.made[id] = wt
	return wt, nil
}

// List 当前仓库下的受管 worktree(以 git 记录为准,故进程外/重启后创建的也在列)。
func (w *Worktrees) List() []sdk.Worktree {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.listLocked()
}

// listLocked 取列表(调用方持锁)。
func (w *Worktrees) listLocked() []sdk.Worktree {
	dir, err := w.workspaceDir()
	if err != nil {
		return nil
	}
	repo, err := w.repoRootQuiet(dir)
	if err != nil {
		return nil
	}
	out, err := runGit(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	root := baseDir()
	var list []sdk.Worktree
	for _, blk := range strings.Split(out, "\n\n") {
		path, branch := "", ""
		for _, line := range strings.Split(blk, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				path = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			case strings.HasPrefix(line, "branch "):
				branch = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "branch ")), "refs/heads/")
			}
		}
		if path == "" || !within(root, path) {
			continue // 只管数据根内的受管 worktree(用户自建的不动)
		}
		name := filepath.Base(path)
		wt := sdk.Worktree{ID: name, Path: path, Repo: repo, Branch: branch}
		if v, ok := w.made[name]; ok {
			wt.Base, wt.CreatedAt = v.Base, v.CreatedAt
		} else if fi, serr := os.Stat(path); serr == nil {
			wt.CreatedAt = fi.ModTime()
		}
		list = append(list, wt)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	return list
}

// Remove 回收一个受管 worktree(分支保留 —— 未合并的改动仍在分支上)。
func (w *Worktrees) Remove(id string, force bool) error {
	dir, err := w.workspaceDir()
	if err != nil {
		return err
	}
	repo, err := w.repoRoot(dir)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	path := ""
	for _, t := range w.listLocked() {
		if t.ID == id {
			path = t.Path
			break
		}
	}
	if path == "" {
		return fmt.Errorf("host-worktrees: 无此受管 worktree %q(可用 /worktree list 查看)", id)
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	if _, err := runGit(repo, append(args, path)...); err != nil {
		return fmt.Errorf("host-worktrees: 回收失败: %w", err)
	}
	delete(w.made, id)
	return nil
}

// repoRoot 取仓库根(非 git 仓库 → 显式报错,不静默退化)。
func (w *Worktrees) repoRoot(dir string) (string, error) {
	out, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if strings.Contains(err.Error(), "未找到 git") {
			return "", fmt.Errorf("host-worktrees: worktree 隔离需要 git 可执行文件在 PATH 上: %w", err)
		}
		return "", fmt.Errorf("host-worktrees: 工作目录不是 git 仓库(%s),无法隔离运行: %w", dir, err)
	}
	return out, nil
}

// repoRootQuiet 同上,但用于只读列表(非仓库 = 空列表)。
func (w *Worktrees) repoRootQuiet(dir string) (string, error) {
	out, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return out, nil
}

// cmdWorktree /worktree 命令实现。
func (w *Worktrees) cmdWorktree(args []string) (string, error) {
	action := "list"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		action = strings.TrimSpace(args[0])
	}
	switch action {
	case "list":
		list := w.List()
		if len(list) == 0 {
			return "无受管 worktree(隔离子代理运行后在此列出)", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "受管 worktree(%d):\n", len(list))
		for _, t := range list {
			fmt.Fprintf(&b, "  %s  %s  %s  %s\n", t.ID, shortSHA(t.Base), branchOrDash(t.Branch), t.Path)
		}
		b.WriteString("回收: /worktree rm <id> [force];分支保留(未合并的改动仍在分支上)")
		return b.String(), nil
	case "rm":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			return "", fmt.Errorf("用法: /worktree rm <id> [force]")
		}
		id := strings.TrimSpace(args[1])
		force := len(args) > 2 && strings.TrimSpace(args[2]) == "force"
		path, branch := "", ""
		for _, t := range w.List() {
			if t.ID == id {
				path, branch = t.Path, t.Branch
			}
		}
		if err := w.Remove(id, force); err != nil {
			return "", err
		}
		return fmt.Sprintf("已回收 worktree %s(%s);分支 %s 保留 —— 未合并的改动仍在分支上(不再需要可 git branch -D)",
			id, path, branchOrDash(branch)), nil
	}
	return "", fmt.Errorf("用法: /worktree list|rm <id> [force]")
}

// idOptions 二级选择器:受管 worktree ID(rm 时;list 无需二级参数)。
func (w *Worktrees) idOptions(picked []string) []sdk.Option {
	if len(picked) < 2 || picked[1] != "rm" {
		return nil
	}
	var out []sdk.Option
	for _, t := range w.List() {
		out = append(out, sdk.Option{Value: t.ID, Desc: shortSHA(t.Base) + " " + branchOrDash(t.Branch)})
	}
	return out
}

// runGit 执行一次 git 子命令(返回 stdout;失败时把 stderr 首行并入错误)。
func runGit(dir string, args ...string) (string, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("未找到 git 可执行文件: %w", err)
	}
	cmd := exec.Command(bin, append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := firstLine(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// within 判断 p 是否在 root 之内(realpath 归一 —— macOS 上 /tmp 是 /private/tmp 软链,
// git worktree list 报的是真实路径,不归一化会把受管 worktree 判成“数据根之外”而列表全空)。
func within(root, p string) bool {
	root, p = realPath(root), realPath(p)
	return p != root && strings.HasPrefix(p, root+string(filepath.Separator))
}

// realPath 解析软链;路径尚不存在时逐级上溯到最近的存在祖先再拼回剩余部分
// (与 tool-shell/kernel.go resolvePath 同口径:解析不出则回退原路径)。
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil && r != "" {
		return r
	}
	dir, tail := filepath.Clean(p), []string{}
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Clean(p)
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
		dir = parent
		if r, err := filepath.EvalSymlinks(dir); err == nil && r != "" {
			return filepath.Join(append([]string{r}, tail...)...)
		}
	}
}

// sanitizeName 把仓库目录名规范成可安全用于目录/分支的片段。
func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "repo"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// shortSHA 基线 commit 摘要(展示用)。
func shortSHA(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}

// branchOrDash 空分支显示为 "-"(detached)。
func branchOrDash(b string) string {
	if strings.TrimSpace(b) == "" {
		return "-"
	}
	return b
}

// firstLine 取首行(多行 git 错误只暴露第一行,避免刷屏)。
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
