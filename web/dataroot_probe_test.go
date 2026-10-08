// 数据根可写性探针 TTL 复用的护栏(批三)。
//
// 这层错了会怎样:缓存过久 = 用户 chmod 之后提示条还在(说「数据根只读」而其实能写);
// 缓存不命中 = 白建白删临时文件(本批要治的病)。两端都要钉。
package web

import (
	"os"
	"runtime"
	"testing"
	"time"
)

// needPermBits 只读目录能否构造出来:Windows 用 ACL 而非权限位,chmod 0o500 不产生
// 只读语义(TempDir 仍可写)⇒ 那三个「改成只读再探」的断言在 Windows 上无对象。
// 与既有 TestProbeWritableReadOnly 同一守卫,不新造口径。
func needPermBits(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root 无视权限位,只读断言无意义")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 用 ACL 而非权限位,chmod 不产生只读语义")
	}
}

// newProbe 造一个独立探针(不碰包级单例,免得用例之间互相影响)。
func newProbe(ttl time.Duration, now func() time.Time) *writableProbe {
	return &writableProbe{ttl: ttl, nowFunc: now}
}

func TestProbeWritableNilWhenNoRoot(t *testing.T) {
	if got := probeWritable(""); got != nil {
		t.Fatalf("root 为空应报 nil(无法判定),got %v", *got)
	}
}

func TestProbeWritableDetectsReadOnlyDir(t *testing.T) {
	needPermBits(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	got := probeWritable(dir)
	if got == nil || *got {
		t.Fatalf("只读目录应报不可写,got %v", got)
	}
}

func TestProbeCacheReusesWithinTTL(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	p := newProbe(5*time.Second, func() time.Time { return now })

	first := p.get(dir)
	if first == nil || !*first {
		t.Fatalf("可写目录应报可写,got %v", first)
	}
	// TTL 内:即便把目录改成只读,也不该重新探测(这正是省 IO 的地方)。
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	second := p.get(dir)
	if second == nil || !*second {
		t.Fatalf("TTL 内应复用旧结论(仍报可写),got %v", second)
	}
}

// TTL 到期后必须重新探测:用户 chmod 了要能看见。
func TestProbeCacheRefreshesAfterTTL(t *testing.T) {
	needPermBits(t)
	dir := t.TempDir()
	now := time.Now()
	p := newProbe(5*time.Second, func() time.Time { return now })

	if got := p.get(dir); got == nil || !*got {
		t.Fatalf("预热应报可写,got %v", got)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	now = now.Add(6 * time.Second)
	if got := p.get(dir); got == nil || *got {
		t.Fatalf("TTL 到期后应重新探测并报不可写,got %v", got)
	}
}

// 换了数据根必须重新探:缓存按 root 记账,不按时间。
func TestProbeCacheKeyedByRoot(t *testing.T) {
	needPermBits(t)
	a, b := t.TempDir(), t.TempDir()
	now := time.Now()
	p := newProbe(time.Hour, func() time.Time { return now })

	if got := p.get(a); got == nil || !*got {
		t.Fatalf("a 应可写")
	}
	if err := os.Chmod(b, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(b, 0o700) })
	if got := p.get(b); got == nil || *got {
		t.Fatalf("换 root 必须重新探(b 应不可写),got %v", got)
	}
}

// 探针不得留残file:两次探测之后目录里不应出现任何 .gah-write-probe-*。
func TestProbeLeavesNoResidue(t *testing.T) {
	dir := t.TempDir()
	_ = probeWritableOnce(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if len(e.Name()) >= 17 && e.Name()[:17] == ".gah-write-probe-" {
			t.Fatalf("探针留下残file:%s", e.Name())
		}
	}
}
