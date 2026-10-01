//go:build windows

package xlock

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryLockFile LockFileEx 独占锁(不阻塞)。
//
// 为什么 Windows 必须走 LockFileEx:O_EXCL 锁文件表达不了"持锁者还活着"——
// 进程被强杀后文件还在,锁看起来一直被占;LockFileEx 随句柄/进程释放(§注)。
// 另一个坑:同一进程内重复对同一文件加锁会被 flock 视为**同一把锁**(unix 语义),
// 而 Windows 上必须用**不同字节区间**才拿得到第二把 —— 统一取 offset 0 的单区间。
func tryLockFile(f *os.File) (bool, error) {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol)
	if err == nil {
		return true, nil
	}
	if err == windows.ERROR_LOCK_VIOLATION {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
