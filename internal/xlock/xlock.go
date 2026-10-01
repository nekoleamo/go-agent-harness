// internal/xlock:跨进程文件锁(多实例并行的地基,第一百零四批)。
//
// 为什么需要:桌面壳多开后,同一个数据根(便携纪律:数据根唯一 = 二进制同级 gah-data/)
// 会被**两个进程**同时读写。进程内的 sync.Mutex 挡不住这件事,而"多开之后定时计划
// 跑两遍""切审批档偶发不生效"这类症状隔着好几层,归因极难 —— 所以在真正会出事的两处
// (调度循环、偏好读-改-写)上先上锁。
//
// 平台口径:
//   - Unix(macOS/Linux):flock(LOCK_EX|LOCK_NB);
//   - Windows:LockFileEx(独占、不阻塞)。**不能**用 O_EXCL 建锁文件了事 —— 那只能表达
//     "我建过",表达不了"我还活着";持锁进程被强杀后锁会一直被占用(Windows 上更明显:
//     句柄随进程释放,但 O_EXCL 文件不会自己消失)。
//
// 锁的性质(两条都要记住):
//
//	① **随进程/句柄自动释放**:进程崩了锁自动松(OS 级语义),不会像"锁文件"那样留垃圾;
//	② **不跨重启记忆**:重启后旧锁不存在,可正常获取 —— 正是我们要的行为。
package xlock

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Handle 持有的锁;Release 后不可再用。
type Handle struct {
	f *os.File
	// key 归一化后的锁路径(进程内记账用)
	key string
}

// 进程内互斥表:同进程内对同一路径只允许一把。
//
// 为什么需要它:**flock 在同一进程内不是互斥的** —— 同一进程两次打开同一文件再 flock,
// 第二次会**直接成功**(内核按「open file description」判定,两个 fd 指向同一个)。
// 于是"同进程的第二个持有者"完全看不见第一个,busy 永远是 false,拿锁的代码
// (调度租约、偏好写)会以为自己独占了。Windows 的 LockFileEx 反而会按区间冲突。
// 这一层把语义拉齐到「跨平台一致:一路径一把锁」。
var inProc = struct {
	sync.Mutex
	held map[string]bool
}{held: map[string]bool{}}

// TryLock 尝试取 path 上的独占锁(不阻塞)。
// 返回 (nil, nil) = **别人持着**(不是错误);拿到时必须 Release。
//
// 注意:调用方应确保 path 所在目录存在(锁文件本身会被创建)。
func TryLock(path string) (h *Handle, busy bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, fmt.Errorf("xlock: 建目录失败: %w", err)
	}
	// 进程内先占位(拿不到就直接返回 busy,不碰文件)
	key := filepath.Clean(path)
	inProc.Lock()
	if inProc.held[key] {
		inProc.Unlock()
		return nil, true, nil
	}
	inProc.held[key] = true
	inProc.Unlock()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		releaseInProc(key)
		return nil, false, fmt.Errorf("xlock: 开锁文件失败: %w", err)
	}
	locked, err := tryLockFile(f)
	if err != nil {
		f.Close()
		releaseInProc(key)
		return nil, false, fmt.Errorf("xlock: 加锁失败: %w", err)
	}
	if !locked {
		f.Close()
		releaseInProc(key)
		return nil, true, nil
	}
	return &Handle{f: f, key: key}, false, nil
}

// releaseInProc 释放进程内占位(取 OS 锁失败/出错的回滚路径)。
func releaseInProc(key string) {
	inProc.Lock()
	delete(inProc.held, key)
	inProc.Unlock()
}

// Release 释放锁并关闭句柄(幂等:重复调用直接返回)。
func (h *Handle) Release() {
	if h == nil || h.f == nil {
		return
	}
	_ = unlockFile(h.f)
	_ = h.f.Close()
	inProc.Lock()
	delete(inProc.held, h.key)
	inProc.Unlock()
	h.f = nil
}

// TryRun 拿锁 → 跑 fn → 释放;拿不到(busy)返回 ran=false,nil。
// 语义:调用方把 ran=false 理解成"这次没轮到我",据此静默跳过或如实告知,**不当作错误**。
func TryRun(path string, fn func() error) (ran bool, err error) {
	h, busy, err := TryLock(path)
	if err != nil || busy {
		return false, err
	}
	defer h.Release()
	return true, fn()
}
