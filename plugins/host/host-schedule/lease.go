package hostschedule

// 调度租约(第一百零四批):多实例共享数据根时,定时计划只能由**一个**实例触发。
//
// 为什么需要:桌面壳多开后,两个 sidecar 进程读同一份 $GAH_HOME/schedules/*.yaml,
// 各自排期、各自 fire —— 同一个 cron 会被跑两遍(两次模型开销 + 两次副作用),
// 而且回写 next_run/last_status 还会互相覆盖。
//
// 为什么用**文件锁**而不是"锁文件 + 心跳":
//   - 文件锁(flock / LockFileEx)随进程与句柄自动释放 —— 持锁实例被强杀/崩溃后,
//     另一个实例立刻能接手,不留需要清理的垃圾;
//   - "锁文件 + 心跳"方案在进程被 SIGKILL 时会留下一个看起来还活着的租约,
//     还得写超时接管与时钟偏移处理。
// 代价:持锁者活着时其它实例看不到"是谁在跑",所以诊断信息靠启动日志 + instances.json。

import (
	"fmt"
	"path/filepath"

	"github.com/nekoleamo/go-agent-harness/internal/xlock"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// leasePath 租约锁文件路径($GAH_HOME/schedules/.lease,与计划文件同目录 → 随目录迁移)。
func leasePath() string {
	return filepath.Join(sdk.Home(), "schedules", ".lease")
}

// acquireLease 取调度租约。busy = 已有实例在跑(不是错误)。
func acquireLease() (h *xlock.Handle, busy bool, err error) {
	h, busy, err = xlock.TryLock(leasePath())
	if err != nil {
		return nil, false, fmt.Errorf("取租约: %w", err)
	}
	return h, busy, nil
}
