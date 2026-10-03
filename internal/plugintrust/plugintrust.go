// Package plugintrust:外部插件的**哈希白名单**($GAH_HOME/plugins/SHA256SUMS)。
//
// 为什么需要它(2026-10-03 调研结论):进程型插件此前**零安装校验** —— 放进
// `$GAH_HOME/plugins/` 的任何二进制都会被 `exec`。围栏(kernelsandbox)限制它**能碰哪些
// 路径**,但不改**它是什么**;而 plugins/ 在数据根下、与凭据目录同级,拿到写权限就能提权。
// 于是「能不能安装别人写的插件」这件事当时没有可陈述的安全边界。
//
// 形态照抄**已在用**的那套:外部插件产物本来就带 SHA256SUMS(internal/embed,
// `internal/embed/extplugins/<平台>/SHA256SUMS`),解析格式与语义完全一致 ——
// 不新造机制,只是把同一个约定从「发行侧自检」延伸到「用户装进来的插件」。
//
// 语义:
//   - **文件不存在** ⇒ 不启用(Load 返回 Enforced=false)。这是「清单被误删」时的
//     安全底:整个机制建立在清单上,清单没了还继续放行等于把它架空。
//   - **文件存在** ⇒ 强制:每个插件二进制都必须**在清单里且哈希一致**,否则跳过并记
//     ERROR(带可直接复制执行的补救命令)。未列入与哈希不符都是 fail-closed。
//   - 插件二进制名必须以 `tool-` / `cmd-` 开头(与 host-bridge 的 isExternalPluginBin
//     同一口径),否则根本不会被加载,清单里写它也没有意义。
//
// 写入方:internal/embed 释放官方四件时登记自己;`gah -install-plugin` 装完登记;
// `gah -trust-plugin` 给已经放在那里的二进制补登记(哈希不符时**不**自动改写清单 ——
// 那等于把被篡改的东西洗白)。
package plugintrust

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileName 白名单文件名(与发行侧 internal/embed 的清单同名同格式)。
const FileName = "SHA256SUMS"

// List 白名单(键 = 插件二进制基名,含平台扩展名;如 tool-basic / tool-kit.exe)。
type List struct {
	dir      string
	enforced bool
	sums     map[string][sha256.Size]byte
}

// Path 白名单绝对路径。
func Path(dir string) string { return filepath.Join(dir, FileName) }

// Load 读白名单。**文件不存在 ⇒ 未启用**(不是错误)。
func Load(dir string) (*List, error) {
	raw, err := os.ReadFile(Path(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return &List{dir: dir, sums: map[string][sha256.Size]byte{}}, nil
		}
		return nil, err
	}
	l := &List{dir: dir, enforced: true, sums: map[string][sha256.Size]byte{}}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 2*sha256.Size {
			return nil, fmt.Errorf("plugintrust: %s 有坏行:%q", FileName, line)
		}
		b, err := hex.DecodeString(fields[0])
		if err != nil || len(b) != sha256.Size {
			return nil, fmt.Errorf("plugintrust: %s 哈希非法(%q)", FileName, fields[0])
		}
		var sum [sha256.Size]byte
		copy(sum[:], b)
		l.sums[fields[1]] = sum
	}
	if len(l.sums) == 0 {
		// 存在但为空 = 「什么都不许装」,这几乎一定是误操作(文件被清空/写坏)。
		// 按 fail-closed 处理并**显式报错** —— 静默成「未启用」等于把机制架空。
		return nil, fmt.Errorf("plugintrust: %s 是空的(什么都不许装)。删掉该文件即可回到「不校验」,或用 gah -install-plugin / gah -trust-plugin 登记", Path(dir))
	}
	return l, nil
}

// Enforced 白名单是否在强制。false = 文件不存在,宿主照旧加载全部(并如实告知一次)。
func (l *List) Enforced() bool { return l != nil && l.enforced }

// Names 已登记的插件名(排序,供诊断/UI 回显)。
func (l *List) Names() []string {
	if l == nil {
		return nil
	}
	out := make([]string, 0, len(l.sums))
	for n := range l.sums {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Verify 校验一个插件二进制。未启用 ⇒ 放行(并说明);启用 ⇒ 必须在清单里且哈希一致。
//
// 返回的 error 文案带**可直接复制执行的补救命令** —— 拦住一个用户自己装的插件却不说
// 怎么放行,等于制造「插件莫名消失」。
func (l *List) Verify(name string, sum [sha256.Size]byte) error {
	if !l.Enforced() {
		return nil
	}
	want, ok := l.sums[name]
	switch {
	case !ok:
		return fmt.Errorf("插件 %s 不在 %s 白名单里(拒绝加载)。确认来源可信后执行:gah -trust-plugin %s", name, FileName, name)
	case want != sum:
		return fmt.Errorf("插件 %s 的哈希与 %s 不符(拒绝加载,文件可能已被改动)。"+
			"若这是你自己重新编译的,执行:gah -trust-plugin %s", name, FileName, name)
	}
	return nil
}

// Sum 已登记的哈希(name 不存在 → ok=false)。
func (l *List) Sum(name string) ([sha256.Size]byte, bool) {
	if l == nil || l.sums == nil {
		return [sha256.Size]byte{}, false
	}
	s, ok := l.sums[name]
	return s, ok
}

// Record 登记/更新一个插件的哈希(upsert),原子写。
//
// **只在校验通过后调用**:本函数不做任何判断,调用方负责决定"这个哈希值可不可信"。
// `-trust-plugin` 因此在哈希不符时**不**自动洗白 —— 见包注释。
func (l *List) Record(name string, sum [sha256.Size]byte) error {
	if err := l.loadIfNeeded(); err != nil {
		return err
	}
	l.sums[name] = sum
	return l.write()
}

// Remove 从白名单移除一个条目(卸载插件时用)。
func (l *List) Remove(name string) error {
	if err := l.loadIfNeeded(); err != nil {
		return err
	}
	if _, ok := l.sums[name]; !ok {
		return nil // 没登记过:幂等
	}
	delete(l.sums, name)
	return l.write()
}

// Set 用一批条目整体替换(embed 首次登记自己时用;已存在的条目原样保留)。
func (l *List) Set(names []string, sum func(name string) ([sha256.Size]byte, bool)) error {
	if err := l.loadIfNeeded(); err != nil {
		return err
	}
	for _, n := range names {
		if s, ok := sum(n); ok {
			l.sums[n] = s
		}
	}
	return l.write()
}

func (l *List) loadIfNeeded() error {
	if l.dir == "" {
		return fmt.Errorf("plugintrust: 白名单目录未设置")
	}
	if l.sums != nil {
		return nil
	}
	cur, err := Load(l.dir)
	if err != nil {
		return err
	}
	l.dir, l.sums, l.enforced = cur.dir, cur.sums, true
	return nil
}

// write 原子写(同目录临时文件 + rename),权限 0644。
//
// 为何不是 0600:这份清单不是凭据,而且它要被用户读、被别的进程核对;
// 真正的凭据在 config/ 下且是 0600。写错权限的后果是「插件莫名加载不了」。
func (l *List) write() error {
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(l.sums))
	for n := range l.sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# gah 外部插件白名单(每行:内容 sha256 + 插件二进制基名)\n")
	b.WriteString("# 由 internal/embed(官方插件)、gah -install-plugin、gah -trust-plugin 写入。\n")
	b.WriteString("# 本文件存在即强制:未列入或哈希不符的插件会被拒绝加载。\n")
	for _, n := range names {
		sum := l.sums[n]
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), n)
	}
	path := Path(l.dir)
	tmp, err := os.CreateTemp(l.dir, ".SHA256SUMS-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	l.enforced = true
	return nil
}

// HashFile 读文件并算 sha256(登记用;流式,不把整个二进制读进内存)。
func HashFile(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
