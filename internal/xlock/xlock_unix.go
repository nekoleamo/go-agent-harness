//go:build !windows

package xlock

import (
	"os"
	"syscall"
)

// tryLockFile flock(LOCK_EX|LOCK_NB):成功 = true;EWOULDBLOCK = 别人持着。
// 选 flock 而不是 fcntl:flock 随 fd 关闭自动释放,且在同一台机器的多个进程间有效。
func tryLockFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
