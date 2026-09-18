// worktree 隔离契约(S-P1-4):受管 git worktree 的宿主服务与隔离运行请求。
// 目的:并行子代理/任务在同一份工作区上互相覆盖文件(同名文件后写者胜)—— 隔离使每个
// 执行者拿到独立工作目录与分支,改动不落主工作区,由发起方显式决定合并/丢弃。
package sdk

import (
	"context"
	"time"
)

// Worktree 一个受管 git worktree(隔离运行的工作目录)。
type Worktree struct {
	ID     string `json:"id"`
	Path   string `json:"path"`   // 工作目录(绝对;隔离运行的 cwd 与沙箱写范围)
	Repo   string `json:"repo"`   // 源仓库根(合并/回收在同一仓库内进行)
	Branch string `json:"branch"` // 隔离分支(改动落点;未合并前不要删)
	Base   string `json:"base"`   // 基线 commit(创建时的 HEAD)
	// CreatedAt 创建时间(展示/清理参考)。
	CreatedAt time.Time `json:"created_at"`
}

// WorktreeService 服务(ctx.worktrees):受管 git worktree 生命周期。
//
// 落点 = `$GAH_HOME/worktrees/<仓库名>-<id>`(便携纪律:gah 产生的数据一律在数据根内);
// **不污染用户仓库** —— 不在仓库内建目录、不改 .gitignore/info/exclude。
// 非 git 仓库 / 无 git 可执行文件 → 显式报错(不静默退化为非隔离运行)。
// 回收策略默认**保留**(改动未合并即删 = 静默丢活):回收必须显式 Remove(或 /worktree rm)。
type WorktreeService interface {
	// Create 新建一个受管 worktree(基于当前 HEAD 新建分支;label 仅作标识/展示)。
	Create(ctx context.Context, label string) (Worktree, error)
	// List 当前仓库下的受管 worktree(含进程外历史创建,按 git 记录推导)。
	List() []Worktree
	// Remove 回收一个受管 worktree(分支保留:未合并的改动仍在分支上,不静默丢弃)。
	// force = true 时丢弃 worktree 内未提交/未跟踪改动(--force)。
	Remove(id string, force bool) error
}
