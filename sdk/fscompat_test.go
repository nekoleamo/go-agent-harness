package sdk

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// renameRec 记录 rename / chmod / sleep 的调用序列,并按 fail 决定前 N 次 **rename** 失败
// (只数 rename 本身 —— chmod/sleep 也走 append,不能混进同一个计数)。
type renameRec struct {
	calls [][]string
	fail  int // 前 N 次 rename 返回 err(0 = 全部成功)
	err   error
	n     int // 已发生的 rename 次数
}

func (r *renameRec) rename(tmp, dst string) error {
	r.n++
	r.calls = append(r.calls, []string{"rename:" + tmp + "->" + dst})
	if r.n <= r.fail {
		return r.err
	}
	return nil
}

func (r *renameRec) chmods(path string, mode os.FileMode) error {
	r.calls = append(r.calls, []string{"chmod:" + path, mode.String()})
	return nil
}

func (r *renameRec) slept(d time.Duration) { r.calls = append(r.calls, []string{"sleep", d.String()}) }

func (r *renameRec) count(prefix string) int {
	n := 0
	for _, c := range r.calls {
		if strings.HasPrefix(c[0], prefix) {
			n++
		}
	}
	return n
}

// 钉住 POSIX 侧的逐字等价:只调一次 rename,失败原样上抛(Windows 的两招不得外溢到别的平台)。
func TestReplaceFilePosixIsPlainRename(t *testing.T) {
	rec := &renameRec{}
	if err := replaceFileWith("t", "d", rec.rename, rec.chmods, rec.slept, false); err != nil {
		t.Fatalf("成功路径不应报错: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("只能调一次 rename,实际 %d 次: %v", len(rec.calls), rec.calls)
	}

	boom := errors.New("EXDEV")
	rec2 := &renameRec{fail: 1, err: boom}
	err := replaceFileWith("t", "d", rec2.rename, rec2.chmods, rec2.slept, false)
	if !errors.Is(err, boom) {
		t.Fatalf("失败必须原样上抛(不吞、不换语义),实际 %v", err)
	}
	if len(rec2.calls) != 1 {
		t.Fatalf("失败不得重试,实际 %v", rec2.calls)
	}
}

// 钉住 Windows 侧的完整序列:清只读位 → 退避重试 → 瞬时占用过去后成功。
func TestReplaceFileWindowsClearsReadonlyThenRetries(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(dst, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &renameRec{fail: 2, err: errors.New("SHARING_VIOLATION")}
	if err := replaceFileWith("t", dst, rec.rename, rec.chmods, rec.slept, true); err != nil {
		t.Fatalf("瞬时占用应重试到成功: %v", err)
	}
	if got := rec.count("chmod:"); got != 1 {
		t.Fatalf("只读位只清一次,实际 %d 次: %v", got, rec.calls)
	}
	if !strings.HasPrefix(rec.calls[1][0], "chmod:"+dst) {
		t.Fatalf("第二次调用应是清目标只读位,实际 %v", rec.calls)
	}
	if rec.count("sleep") != 2 {
		t.Fatalf("两次重试之间都要退避,实际 %v", rec.calls)
	}
}

// 一直失败 ⇒ 交出最后一次错误,并在文案里说明做过什么(区分"瞬时占用"与"真没权限")。
func TestReplaceFileWindowsGivesUpWithLastError(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(dst, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("ACCESS_DENIED")
	rec := &renameRec{fail: 99, err: boom}
	err := replaceFileWith("t", dst, rec.rename, rec.chmods, rec.slept, true)
	if !errors.Is(err, boom) {
		t.Fatalf("必须保留原始错误(errors.Is 可判),实际 %v", err)
	}
	if !strings.Contains(err.Error(), "重试") {
		t.Fatalf("错误文案要说明已重试过,实际 %v", err)
	}
	if got := rec.count("rename:"); got != replaceAttempts+1 {
		t.Fatalf("应为 1 次首发 + %d 次重试,实际 %d: %v", replaceAttempts, got, rec.calls)
	}
}

// 目标不存在 ⇒ 那不是"替换",rename 失败多半是真错误:不重试、不清只读。
func TestReplaceFileMissingTargetIsNotRetried(t *testing.T) {
	rec := &renameRec{fail: 99, err: errors.New("EXDEV")}
	err := replaceFileWith("t", filepath.Join(t.TempDir(), "nope.json"), rec.rename, rec.chmods, rec.slept, true)
	if err == nil {
		t.Fatal("目标不存在时必须报错")
	}
	if len(rec.calls) != 1 {
		t.Fatalf("不得重试/清只读,实际 %v", rec.calls)
	}
}

// 真实文件系统:覆盖既有文件成功,内容与源一致,临时文件被 rename 走(不是删了重建)。
func TestReplaceFileRealReplace(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "a.json")
	if err := os.WriteFile(dst, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "tmp-1")
	if err := os.WriteFile(tmp, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceFile(tmp, dst); err != nil {
		t.Fatalf("ReplaceFile: %v", err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "new" {
		t.Fatalf("内容应为 new,实际 %q", b)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("源临时文件应已被 rename 走,实际 stat err=%v", err)
	}
}

func TestRemoveTreePosixSingleTry(t *testing.T) {
	calls := 0
	err := removeTreeWith("dir", func(string) error { calls++; return nil }, func(string) error {
		t.Fatal("POSIX 成功路径不得清只读")
		return nil
	}, false)
	if err != nil || calls != 1 {
		t.Fatalf("POSIX 上应一次删掉,实际 err=%v calls=%d", err, calls)
	}
}

func TestRemoveTreeWindowsRetriesAfterChmod(t *testing.T) {
	boom := errors.New("ACCESS_DENIED")
	calls, chmods := 0, 0
	err := removeTreeWith("dir", func(string) error {
		calls++
		if calls == 1 {
			return boom
		}
		return nil
	}, func(string) error { chmods++; return nil }, true)
	if err != nil || calls != 2 || chmods != 1 {
		t.Fatalf("应「先试 → 清只读 → 再试」,实际 err=%v calls=%d chmods=%d", err, calls, chmods)
	}
}

// 清不动只读位时交出**原始**删除错误:清只读是手段,删干净才是目的。
func TestRemoveTreeKeepsOriginalErrorWhenChmodFails(t *testing.T) {
	boom := errors.New("ACCESS_DENIED")
	err := removeTreeWith("dir", func(string) error { return boom },
		func(string) error { return errors.New("chmod 也失败") }, true)
	if !errors.Is(err, boom) {
		t.Fatalf("实际 %v", err)
	}
}

func TestRemoveTreeRealRemove(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTree(dir); err != nil {
		t.Fatalf("RemoveTree: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("目录应已删除,实际 stat err=%v", err)
	}
}

// makeWritable 必须把只读位真的清掉,且目录也要清(目录缺 r-x 就进不去,里面的文件删不掉)。
func TestMakeWritableClearsReadonly(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ro := filepath.Join(sub, "ro.txt")
	if err := os.WriteFile(ro, []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	// 目录先造好再收紧(反过来写不进文件):只读目录正是「进不去 ⇒ 里面的文件删不掉」那一档
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := makeWritable(dir); err != nil {
		t.Fatalf("makeWritable: %v", err)
	}
	fi, err := os.Stat(ro)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o200 == 0 {
		t.Fatalf("只读文件的写位应被清掉,实际 mode=%v", fi.Mode().Perm())
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("清完之后整棵树应可删: %v", err)
	}
}

// 并发里目录已消失不算失败。
func TestMakeWritableMissingTreeIsNotAnError(t *testing.T) {
	if err := makeWritable(filepath.Join(t.TempDir(), "gone")); err != nil {
		t.Fatalf("目录已消失应视为成功,实际 %v", err)
	}
}

// 遍历报**非「不存在」**的错误必须上抛;chmod 失败同理。「已不存在」那半支(并发里已被删、
// chmod 打到已消失的条目)反过来必须当成成功。
//
// 全程注入,不碰真实文件系统的 errno 形状:「父级是个文件」在 POSIX 是 ENOTDIR、在 Windows 却被
// 映射成「不存在」(2026-10-10 本仓 test-windows 当场红过一次)。
func TestMakeWritableErrorSemantics(t *testing.T) {
	walkErr := errors.New("permission denied")
	noopChmod := func(string, os.FileMode) error { return nil }
	err := makeWritableWith("root", func(_ string, fn fs.WalkDirFunc) error {
		return fn("root/deep", nil, walkErr)
	}, noopChmod)
	if !errors.Is(err, walkErr) {
		t.Fatalf("遍历错必须上抛(不得当成已处理),实际 %v", err)
	}
	err = makeWritableWith("root", func(_ string, fn fs.WalkDirFunc) error {
		return fn("root/deep", nil, fs.ErrNotExist)
	}, noopChmod)
	if err != nil {
		t.Fatalf("「已不存在」应视为成功(并发删除不是失败),实际 %v", err)
	}
	// 正常条目:不得报错
	err = makeWritableWith("root", func(_ string, fn fs.WalkDirFunc) error {
		if e := fn("root/file", fakeDirEntry{isDir: false}, nil); e != nil {
			return e
		}
		return fn("root/dir", fakeDirEntry{isDir: true}, nil)
	}, noopChmod)
	if err != nil {
		t.Fatalf("正常条目不应报错: %v", err)
	}
	// chmod 失败(非「不存在」)必须上抛
	chmodErr := errors.New("chmod boom")
	err = makeWritableWith("root", func(_ string, fn fs.WalkDirFunc) error {
		return fn("root/file", fakeDirEntry{}, nil)
	}, func(string, os.FileMode) error { return chmodErr })
	if !errors.Is(err, chmodErr) {
		t.Fatalf("chmod 错必须上抛(不得当成已处理),实际 %v", err)
	}
	// chmod 打到已消失的条目(ErrNotExist)→ 当成功(与遍历同一口径)
	err = makeWritableWith("root", func(_ string, fn fs.WalkDirFunc) error {
		return fn("root/file", fakeDirEntry{}, nil)
	}, func(string, os.FileMode) error { return fs.ErrNotExist })
	if err != nil {
		t.Fatalf("chmod 打在已消失的条目上应视为成功,实际 %v", err)
	}
}

// fakeDirEntry 只为让 makeWritableWith 的回调拿到一个可控的 DirEntry。
type fakeDirEntry struct{ isDir bool }

func (fakeDirEntry) Name() string  { return "x" }
func (d fakeDirEntry) IsDir() bool { return d.isDir }
func (fakeDirEntry) Type() fs.FileMode {
	return 0
}
func (fakeDirEntry) Info() (fs.FileInfo, error) { return nil, errors.New("未用") }
