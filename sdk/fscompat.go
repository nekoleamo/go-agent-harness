package sdk

// Windows 上「覆盖既有文件」与「递归删除」的两条平台语义补丁(2026-10-10 真机审查登记)。
//
// 为何要有:全仓的原子写都是「同目录临时文件 → rename 覆盖」(15 处),插件卸载都是
// `os.RemoveAll`。这两件事在 POSIX 上**几乎不会失败**,于是代码里从来没写防御;而 Windows 上
// 它们各有一类必然失败的来源:
//
//  ① 目标带**只读属性**(zip 解压保留、构建脚本 `chmod 0444`、从只读源拷贝、单副本备份还原):
//     rename 覆盖与删除都被拒(`ACCESS_DENIED`)。POSIX 上「写保护在文件上、能不能删只看目录权限」
//     ⇒ 同一个文件照样能覆盖/删除 ⇒ 这类失败**只会在 Windows 上出现**。
//  ② 目标**正被别的进程打开**且没共享删除句柄(杀毒实时扫描、索引器、回滚工具、同数据根的第二个
//     gah 实例):rename 报 `SHARING_VIOLATION`。占用通常是**瞬时**的 ⇒ 短暂重试就能过。
//
// 故:POSIX 上这两个函数**逐字等价**于 `os.Rename` / `os.RemoveAll`(不多一次系统调用、行为不变);
// Windows 上多两招(清只读位 + 短重试)。仍失败时把**最后一次**错误原样上抛 —— 不吞错、不静默降级
// (静默降级会让「写档没生效」退化成一行没人看的日志,本仓已因此踩过:Windows 真机报「点了没反应」,
// 而界面当时连一句错误都没有)。
//
// 用途:宿主内部的原子写与删除一律走这两个函数(替换 `os.Rename`/`os.RemoveAll`);
// 在 Windows 上写文件的插件也应当用它们。

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// 重试节奏:5 次、指数退避(10/20/40/80/160ms,合计约 310ms)。
// 取这个量级而不是更长:占用来自杀毒/索引器时是**毫秒级**的瞬时窗口;而调用方(写档)通常是
// 用户在等一次点击反馈,不该为了一个大概率不存在的占用让界面卡上半秒。
const (
	replaceAttempts = 5
	replaceBaseWait = 10 * time.Millisecond
)

// ReplaceFile 把 tmp **原子**放到 dst(覆盖既有文件)。
//
// 与 os.Rename 的差别只在 Windows:先清掉 dst 的只读位,再对「瞬时占用」短重试。
// dst 不存在时不做任何额外动作(那不是替换,是首建);目标存在但 rename 仍失败且清不掉只读位时,
// 交出原始错误(清只读只是手段,不是目的)。
func ReplaceFile(tmp, dst string) error {
	return replaceFileWith(tmp, dst, os.Rename, os.Chmod, time.Sleep, runtime.GOOS == "windows")
}

// replaceFileWith ReplaceFile 的可测内核(依赖注入:平台判定与三个副作用都作为参数)。
//
// 为什么要拆:「只在 Windows 生效」的那段分支在 macOS/Linux CI 上永远跑不到 —— 直接写在
// ReplaceFile 里,这段代码就**没有任何一条用例能钉住**(本地绿 ≠ Windows 绿,这条坑本仓踩过两次)。
// 拆开后,契约(何时清只读、重试几次、失败怎么报)在**任何平台**都能验。
func replaceFileWith(
	tmp, dst string,
	rename func(string, string) error,
	chmod func(string, fs.FileMode) error,
	sleep func(time.Duration),
	windows bool,
) error {
	err := rename(tmp, dst)
	if err == nil || !windows {
		// 非 Windows:rename 失败必是真错误(跨设备 / 权限 / 路径非法),重试只会掩盖它。
		return err
	}
	if _, statErr := os.Lstat(dst); statErr != nil {
		// 目标压根不存在却仍失败 ⇒ 不是「只读 / 被占用」,是真错误,别拿重试糊弄过去。
		return err
	}
	_ = chmod(dst, 0o666) // 清 Windows 只读属性;POSIX 语义下这是"加回写位",无害(仅在已失败时执行)
	var last error
	for i := 1; i <= replaceAttempts; i++ {
		sleep(replaceBaseWait << (i - 1))
		if e := rename(tmp, dst); e == nil {
			return nil
		} else {
			last = e
		}
	}
	return fmt.Errorf("替换 %s 失败(已清只读位并重试 %d 次): %w", dst, replaceAttempts, last)
}

// RemoveTree 递归删除 dir。Windows 上带只读属性的文件会让 `os.RemoveAll` 失败 ⇒
// 先原样试一次,失败再把整棵树的可写位清掉重来一次。
func RemoveTree(dir string) error {
	return removeTreeWith(dir, os.RemoveAll, makeWritable, runtime.GOOS == "windows")
}

// removeTreeWith RemoveTree 的可测内核(同 replaceFileWith 的理由)。
func removeTreeWith(dir string, rm func(string) error, makeWritable func(string) error, windows bool) error {
	err := rm(dir)
	if err == nil || !windows {
		return err
	}
	if werr := makeWritable(dir); werr != nil {
		return err // 清不动就把原始错误交出去:清只读是手段,删干净才是目的
	}
	return rm(dir)
}

// makeWritable 把整棵树的只读位清掉(目录要可进入才能删里面的文件,故目录与文件一起处理)。
func makeWritable(dir string) error {
	return makeWritableWith(dir, filepath.WalkDir, os.Chmod)
}

// makeWritableWith makeWritable 的可测内核(注入遍历器与 chmod)。
//
// 为什么要拆、为什么把 chmod 也注进去(2026-10-10 本仓自己的 Windows CI 当场教过一次):
//
//	① 「非「不存在」的遍历错误必须上抛」那一支在真实文件系统上几乎构造不出来 —— 我们是树的主人,
//	   chmod 子目录永远成功(改子目录只需**父目录**的写权限),于是 WalkDir 不会因权限失败。
//	② 想用「路径的父级是个文件」制造 chmod 失败:POSIX 给 ENOTDIR,但 **Windows 把
//	   ERROR_PATH_NOT_FOUND 映射成「不存在」**⇒ 断言 `errors.Is(err, fs.ErrNotExist)` 在 Windows 上
//	   成立,用例当场红(`test-windows`,2026-10-10)。
//
// 注入之后这些契约在**任何平台**都由用例钉住,不依赖任何「某个 errno 在某个平台上长什么样」。
func makeWritableWith(
	dir string,
	walk func(string, fs.WalkDirFunc) error,
	chmod func(string, fs.FileMode) error,
) error {
	return walk(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // 并发里已被删掉 = 目标达成,不算失败
			}
			return err
		}
		// 0o700 目录:给 rwx(清只读 + 可进入);文件 0o600:清只读。Windows 上这两个值都没有
		// 「只读」以外的副作用 —— Go 在 Windows 只映射只读位,Unix 权限位被忽略。
		mode := fs.FileMode(0o600)
		if d.IsDir() {
			mode = 0o700
		}
		if cerr := chmod(path, mode); cerr != nil && !errors.Is(cerr, fs.ErrNotExist) {
			return cerr
		}
		return nil
	})
}
