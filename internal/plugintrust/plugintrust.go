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
	"time"
)

// FileName 白名单文件名(与发行侧 internal/embed 的清单同名同格式)。
const FileName = "SHA256SUMS"

// AuditEntry 一次登记的审计行(时间 / 来源 / 插件 / 哈希)。
//
// 它**不是安全边界**:能改 plugins/ 的人也能改这份注释(与 SHA256SUMS 本身同性质)。
// 它解决的是「事后说不清是谁、什么时候、从哪放进来的」—— 事故复盘与追责要用,
// 拦不住任何人。同理,注释里的时间/来源可被编辑,不得用于任何判定。
type AuditEntry struct {
	Time   string // RFC3339
	Source string // embed | install:<spec> | trust:manual | ui-install:<spec>
	Name   string
	Hash   string // 64 位小写 hex
}

// auditPrefix 审计注释行的字段前缀(与数据行区分:数据行以 64 位 hex 开头)。
const auditPrefix = "# audit:"

// List 白名单(键 = 插件二进制基名,含平台扩展名;如 tool-basic / tool-kit.exe)。
type List struct {
	dir      string
	enforced bool
	sums     map[string][sha256.Size]byte
	audits   []AuditEntry // 解析自 `# audit:` 注释行(时间/来源;非边界,见类型注释)
}

// Path 白名单绝对路径。
func Path(dir string) string { return filepath.Join(dir, FileName) }

// Load 读白名单。**文件不存在 ⇒ 未启用**(不是错误)。
//
// **两种「空」必须分开**(批四 §A.1,2026-10-03):
//
//	0 字节     ⇒ 坏文件 ⇒ **报错**。几乎必然是被写坏/截断;fail-closed 不变。
//	只有注释行 ⇒ **合法**:启用(Enforced=true),但当前**没有任何被信任插件**。
//
// 为什么必须分:本项目要有「UI 侧无条件建闸」的能力(boot 每次创建一个空清单),
// 而 UI 侧**根本没有官方插件** ——「像进程型那样每次 boot 登记官方件」这条路不存在。
// 要让闸必然存在就只能无条件建一个空文件,而旧口径把「空」与「坏」混为一谈,
// 于是「建闸」与「fail-closed」直接冲突。
//
// 两者**可靠可区分**(0 字节 vs 有内容),不需要猜。口径**两侧统一**
// (进程型也适用 —— 它的清单正常情况下有官方四件,但**规则一致**比**规则各自合理**更重要)。
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
		if strings.HasPrefix(line, auditPrefix) {
			if e, ok := parseAudit(strings.TrimSpace(strings.TrimPrefix(line, auditPrefix))); ok {
				l.audits = append(l.audits, e)
			}
			continue
		}
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
		// 一条数据行都没有 ⇒ 判成「坏文件」还是「合法的空清单」?
		// 判据是**内容**:0 字节是被写坏/截断(fail-closed);有内容(哪怕只是注释)=
		// 「我们建了闸、闸后面暂时没人」(合法且**启用**)。见函数注释。
		if len(strings.TrimSpace(string(raw))) == 0 {
			return nil, fmt.Errorf("plugintrust: %s 是空的(什么都不许装)。删掉该文件即可回到「不校验」,或用 gah -install-plugin / gah -trust-plugin 登记", Path(dir))
		}
	}
	return l, nil
}

// EnsureEmpty 建一个**空的但已启用**的清单(内容 = 文件头注释;幂等)。
//
// 用途:UI 侧要在 boot 时**无条件建闸**(该侧没有官方插件可自登记,所以「像进程型那样
// 每次启动登记官方件」这条路不存在)。建出来的东西必须是「合法空」而不是「0 字节坏文件」——
// 两者可靠可区分(见 Load),不需要额外标记。
//
// 幂等:已存在就**什么都不做**(不覆写别人的内容)。返回 created 表示是不是这次新建的。
func EnsureEmpty(dir string) (created bool, err error) {
	path := Path(dir)
	if _, err := os.Stat(path); err == nil {
		return false, nil // 已有 ⇒ 不动
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	body := listHeader +
		"# 这个清单是**空的**:闸已经建起来,当前没有任何被信任的插件。\n" +
		"# 确认某个插件可信后执行 gah -trust-plugin <名>(UI 侧:gah -trust-ui-plugin <id>)。\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// Open 打开(或新建)某个目录下的白名单,用于**另一处**要复用同一套语义的场景
// (如 ui-plugins/ 与 plugins/ 各有一份清单)。字段不导出,故必须走本构造。
func Open(dir string) *List { return &List{dir: dir, sums: map[string][sha256.Size]byte{}} }

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

// listHeader 清单的文件头(写出侧的唯一文案源;EnsureEmpty 与 write 共用)。
//
// 为什么抽出来:两处写出的注释必须**逐字一致** —— EnsureEmpty 建的是「合法空」,
// write 在条目清空时也写「合法空」,两处文案不同会让用户以为是两种文件。
const listHeader = "# gah 外部插件白名单(每行:内容 sha256 + 插件二进制基名)\n" +
	"# 由 internal/embed(官方插件)、gah -install-plugin、gah -trust-plugin 写入。\n" +
	"# 本文件存在即强制:未列入或哈希不符的插件会被拒绝加载。\n"

// write 原子写(同目录临时文件 + rename),权限 0644。
//
// 为何不是 0600:这份清单不是凭据,而且它要被用户读、被别的进程核对;
// 真正的凭据在 config/ 下且是 0600。写错权限的后果是「插件莫名加载不了」。
func (l *List) write() error {
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return err
	}
	// 清单空了 ⇒ **删文件**,不写空文件。
	//
	// 为何:Load 对「文件存在但一条没有」是 fail-closed(报错),因为那份状态几乎必然是
	// 文件被清空/写坏 —— 但「卸载了最后一个插件」也会落到同一个状态。两者没法靠内容区分,
	// 所以**不写空文件**:空 = 未启用(与升级前一致),坏文件 = 报错。区分放在写入侧。
	names := make([]string, 0, len(l.sums))
	for n := range l.sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(listHeader)
	if len(l.sums) == 0 {
		// **写注释,不是删文件**(2026-10-03 批四改;这是一个真漏洞的修复)。
		//
		// 旧口径在条目清空时**删掉文件**,因为当时的 Load 把「文件存在但一条没有」
		// 判为「坏文件 ⇒ 报错」—— 写出一个空文件会变成「gah 起不来」。
		// 于是「删文件」成了唯一不产生坏文件的选择。
		//
		// 后果(批四引入「合法空」语义后才暴露出来):**撤销最后一条登记 = 把闸关掉**。
		// `Load` 对不存在的文件返回 `Enforced()==false` ⇒ 手工放置的 UI 插件立刻又能加载,
		// 直到下一次 boot 由 EnsureUIList 重建闸为止 —— 一个**静默的、只在你撤销时
		// 发生**的口子,而且窗口长度取决于你什么时候重启。
		//
		// 现在 Load 已能区分「0 字节(坏)」与「只有注释(合法且启用)」,所以这里直接
		// 写注释:文件仍在 ⇒ Enforced 仍为 true ⇒ 闸一直关着,直到有人真的登记了什么。
		b.WriteString("# 这个清单是**空的**:闸已建起,当前没有任何被信任的插件。\n")
		b.WriteString("# 确认某个插件可信后执行 gah -trust-plugin <名>(UI 侧:gah -trust-ui-plugin <id>)。\n")
	}
	for _, n := range names {
		sum := l.sums[n]
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), n)
	}
	// 审计行在数据行之后(数据行在前 = 人与解析器先看到「装了什么」)。
	// source 可能含空格(repo spec 带分支名),故按固定次序解析:时间、来源(末两段固定)、
	// 名、哈希 —— 写成 `<时间> <source> <名> <哈希>` 且 source 内部空格会被 Fields 拆开,
	// 故这里把 source 里的空白替换掉(审计不需要精确到带空格的 URL)。
	for _, a := range l.audits {
		fmt.Fprintf(&b, "%s %s %s %s %s\n", auditPrefix, a.Time,
			strings.ReplaceAll(a.Source, " ", "_"), a.Name, a.Hash)
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

// parseAudit 解析 `# audit: <RFC3339> <source> <name> <hash>`。
// 任一字段不合规 → ok=false(**跳过而不报错**):审计行是注释,坏注释不该让整份清单失效
// (那会把「一条手改坏的注释」变成「所有插件都加载不了」)。
func parseAudit(body string) (AuditEntry, bool) {
	f := strings.Fields(body)
	if len(f) != 4 || len(f[3]) != 2*sha256.Size {
		return AuditEntry{}, false
	}
	if _, err := time.Parse(time.RFC3339, f[0]); err != nil {
		return AuditEntry{}, false
	}
	return AuditEntry{Time: f[0], Source: f[1], Name: f[2], Hash: f[3]}, true
}

// Audit 已登记的审计行(按写入顺序)。
func (l *List) Audit() []AuditEntry {
	if l == nil {
		return nil
	}
	out := make([]AuditEntry, len(l.audits))
	copy(out, l.audits)
	return out
}

// LastAuditOf 某个插件最近一次登记的审计行(找不到 → ok=false)。
func (l *List) LastAuditOf(name string) (AuditEntry, bool) {
	if l == nil {
		return AuditEntry{}, false
	}
	for i := len(l.audits) - 1; i >= 0; i-- {
		if l.audits[i].Name == name {
			return l.audits[i], true
		}
	}
	return AuditEntry{}, false
}

// RecordWithAudit 登记 + 追加一条审计行(时间 = 现在;source 由调用方给,如
// `install:github.com/foo/bar@v1` / `trust:manual` / `embed`)。
//
// 为什么 Record 与审计一起做:分开就会出现「登记了但没审计」的状态,而那份清单正是
// 事后唯一能说清来源的东西。
func (l *List) RecordWithAudit(name string, sum [sha256.Size]byte, source string) error {
	if err := l.loadIfNeeded(); err != nil {
		return err
	}
	l.sums[name] = sum
	l.audits = append(l.audits, AuditEntry{
		Time:   time.Now().UTC().Format(time.RFC3339),
		Source: source,
		Name:   name,
		Hash:   hex.EncodeToString(sum[:]),
	})
	return l.write()
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
