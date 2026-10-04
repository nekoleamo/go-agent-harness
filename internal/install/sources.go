// sources.go:插件**来源账**($GAH_HOME/plugins/sources.yaml,0600)。
//
// 要解决的问题(批一,2026-10-03):`gah -install <repo>[@ref]` 是 `git clone --depth 1 --branch X`,
// **没有任何固定**。同名 tag 可被 force-push 覆盖;GitHub/Gitee 的用户名可以注销后被重新注册
// (原作者放弃插件 → 账号被回收 → 新人注册同名账号推一个「v1.2.0」)。于是用户重跑**一模一样的
// 命令**就装到了别人的代码,全程无痕。SHA256SUMS 挡不住这件事 —— 它只管「装完有没有被换」,
// 不管「装的那一刻拿到的是不是同一份」。
//
// 与 SHA256SUMS 的关系(刻意**不是**合并成一份):
//   - SHA256SUMS 按**二进制**,随插件增删;管的是「盘上这份还没被换」。
//   - sources.yaml 按**仓库**,只在安装时更新;管的是「我当初装的是哪一份代码」。
//
// 两者生命周期不同、键空间不同,合成一份会让任一方想改键时互相破坏。
//
// 为什么独立文件而不是往 `# audit:` 行里加字段:`internal/plugintrust` 的 `parseAudit` 要求
// **恰好 4 个字段**,多一个会**静默丢弃整条审计行**(改的是 pkg 内那条解析器,两处耦合;
// 独立文件是刻意选择,见 §「回归护栏」)。
//
// 本文件**不是安全边界**:能改 plugins/ 的人也能改这份账。它防的是**自己重跑命令**时
// 不知不觉地换了一份代码 —— 那个场景不需要攻击者,只需要仓库作者易手。
package install

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SourcesFile 来源账文件名(与 SHA256SUMS 同目录、同性质)。
const SourcesFile = "sources.yaml"

// 来源种类。
const (
	// KindTag 已固定的一版(作者表达「这一版就是这一版」)⇒ 漂移即拒绝。
	KindTag = "tag"
	// KindBranch / KindDefault 会移动的引用 ⇒ 漂移放行但记账。
	KindBranch  = "branch"
	KindDefault = "default"
	// KindCommit 40 位 sha 固定 ⇒ 解析结果理应一致(见 CheckDrift 的说明)。
	KindCommit = "commit"
	// KindLocal 本地目录(不是 git 仓库)⇒ 无 ref 可比。
	KindLocal = "local"
)

// 来源归属(批四的分类展示用)。
const (
	OriginUser     = "user"
	OriginOfficial = "official"
)

// SourceEntry 一条来源记录。
//
// 形状与 1.2 的守卫直接相关:**Repo 必须存归一化后的**。四种拼法
// (`https://github.com/a/b` / `git@github.com:a/b.git` / `github.com/a/b` / 带尾斜杠)
// 不归一化就是四条不同记录,守卫形同虚设。
type SourceEntry struct {
	Repo string `yaml:"repo"`          // 归一化:github.com/a/b(无 scheme,host 小写,端口保留)
	Ref  string `yaml:"ref,omitempty"` // tag/branch 名或 40 位 sha;kind=default 时为空
	Kind string `yaml:"kind"`          // tag | branch | default | commit | local
	// Commit 实际解析到的 40 位 sha。解析不到时为 ""(不是 "unknown" —— 空即「未解析」,
	// 写死一个字符串会让人分不清「真的解析失败」与「还没解析」)。前端显示为「未知」。
	Commit string `yaml:"commit,omitempty"`
	// PluginID 插件 id(账的键;同一插件 id 重复安装覆盖)。
	PluginID string `yaml:"plugin_id"`
	// APIVersion 安装时从 manifest 写下的协议版本(给「本地兼容性提示」用;见 §1.6)。
	APIVersion string `yaml:"api_version,omitempty"`
	Origin     string `yaml:"origin,omitempty"` // user | official
	// LocalPath 本地目录安装时的绝对路径(kind=local 时有值)。
	LocalPath string `yaml:"local_path,omitempty"`
	// Prebuilt 预编译产物的下载 URL(批三)。非空 = 这次装的是**下载来的二进制**,
	// 不是在你机器上构建的 —— 这是一条**信任语义**上的区别,必须能被事后翻出来。
	Prebuilt    string `yaml:"prebuilt,omitempty"`
	InstalledAt string `yaml:"installed_at"` // RFC3339 UTC
	Drifted     bool   `yaml:"drifted,omitempty"`
}

// sourcesFile 来源账的文件结构。
type SourceLedger struct {
	Sources []SourceEntry `yaml:"sources"`
}

// SourcesPath 来源账绝对路径。
func SourcesPath(home string) string { return filepath.Join(home, "plugins", SourcesFile) }

// LoadSources 读来源账。**文件不存在 ⇒ 空账**(不是错误)。
//
// 坏文件的处置(回归护栏,必须钉住,不能既不报错也不装、也不能静默覆盖旧记录):
// 这里**返回错误**,由调用方决定。用户此时最需要的不是「继续」而是「别弄丢我装过什么」——
// 静默当成空账的话,下一次安装就会 WriteSources 覆盖掉整份旧记录,而那份记录正是事后唯一
// 能回答「上周那个插件是哪一份」的依据。**报错是唯一不丢信息的做法**。
func LoadSources(home string) (*SourceLedger, error) {
	sf := &SourceLedger{}
	b, err := os.ReadFile(SourcesPath(home))
	if err != nil {
		if os.IsNotExist(err) {
			return sf, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(b, sf); err != nil {
		return nil, fmt.Errorf("install: %s 解析失败(不覆盖它 —— 这份账是「装过哪些来源」的唯一依据): %w",
			SourcesPath(home), err)
	}
	return sf, nil
}

// WriteSources 原子写来源账(0600)。
//
// 为何 0600:这份账不是凭据,但它含你装了哪些第三方仓库 —— 别人读到就等于知道你依赖什么。
// 同目录的 SHA256SUMS 是 0644(它要被别的进程核对),两者性质不同。
func WriteSources(home string, sf *SourceLedger) error {
	dir := filepath.Join(home, "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if sf.Sources == nil {
		sf.Sources = []SourceEntry{}
	}
	b, err := yaml.Marshal(sf)
	if err != nil {
		return err
	}
	header := "# gah 插件来源账(自动维护;「我当初装的是哪一份代码」的唯一记录)\n" +
		"# repo 一律存归一化后的 host/path;commit 为安装时解析到的 40 位 sha。\n"
	tmp, err := os.CreateTemp(dir, ".sources-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.WriteString(header + string(b)); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, SourcesPath(home)); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Sources 全部来源记录(排序后;按 PluginID)。
func (s *SourceLedger) All() []SourceEntry {
	if s == nil {
		return nil
	}
	out := make([]SourceEntry, len(s.Sources))
	copy(out, s.Sources)
	sort.SliceStable(out, func(i, j int) bool { return out[i].PluginID < out[j].PluginID })
	return out
}

// Find 按插件 id 找一条记录。
func (s *SourceLedger) Find(pluginID string) (SourceEntry, bool) {
	if s == nil {
		return SourceEntry{}, false
	}
	for _, e := range s.Sources {
		if e.PluginID == pluginID {
			return e, true
		}
	}
	return SourceEntry{}, false
}

// MustFind 找不到时给零值记录(安装现场只需判「有没有」,不需区分「没找到」与「nil 接收者」)。
func (s *SourceLedger) MustFind(pluginID string) SourceEntry {
	e, _ := s.Find(pluginID)
	return e
}

// Record 记/覆盖一条来源(同 plugin_id 覆盖)。installed_at 由调用方给(便于测试钉住)。
func (s *SourceLedger) Record(e SourceEntry) {
	for i := range s.Sources {
		if s.Sources[i].PluginID == e.PluginID {
			s.Sources[i] = e
			return
		}
	}
	s.Sources = append(s.Sources, e)
}

// Remove 按插件 id 删一条(幂等;卸载时用)。
func (s *SourceLedger) Remove(pluginID string) {
	kept := make([]SourceEntry, 0, len(s.Sources))
	for _, e := range s.Sources {
		if e.PluginID != pluginID {
			kept = append(kept, e)
		}
	}
	s.Sources = kept
}

// NormalizeRepo 仓库 URL 归一化(守卫会不会被绕过,全看这一条)。
//
// 四种拼法是同一个仓库:`https://github.com/a/b`、`git@github.com:a/b.git`、
// `github.com/a/b`、`https://github.com/a/b/`。不归一化,用户换个写法重跑就绕过了守卫。
//
// 步骤(顺序有意义,不能换):
//  1. 记下有无 scheme,并去前缀(http/https/git/ssh 一律去掉 —— 同一仓库)
//  2. scp 形态 `git@host:path` → `host/path`;URL 形态只剥 `user@`,**不动冒号**(那是端口)
//  3. 去尾部 "/"
//  4. 去 ".git" 后缀
//  5. host 小写;**端口保留**(自建 git 常带端口,`git.example.com:8443/a/b` 与无端口是两个仓库)。
//     端口只出现在 **URL 形态**(`https://host:8443/a/b`、`ssh://git@host:8443/a/b.git`)——
//     scp 形态 `git@host:path` 的冒号按 git 自己的规则**恒为路径分隔符**,端口表达不了
//  6. 结果形如 `github.com/a/b`(不带 scheme)
func NormalizeRepo(spec string) string {
	s := strings.TrimSpace(spec)
	hasScheme := false
	for _, p := range []string{"https://", "http://", "git://", "ssh://", "git+https://", "git+ssh://", "file://"} {
		if len(s) >= len(p) && strings.EqualFold(s[:len(p)], p) {
			s, hasScheme = s[len(p):], true
			break
		}
	}
	if !hasScheme {
		// scp 风格:冒号是路径分隔符。必须在去尾斜杠之前 —— `:` 是它唯一的分隔符。
		if i := strings.Index(s, "@"); i > 0 && i < len(s)-1 {
			if j := strings.Index(s[i+1:], ":"); j > 0 {
				s = s[i+1:i+1+j] + "/" + s[i+1+j+1:]
			}
		}
	}
	// URL 形态里的 `user@`(ssh://git@host:port/path):只剥 user@,冒号留给端口。
	// 判据「@ 之前不含 /」:有 / 说明 @ 在路径里(`github.com/a/b@v1` 不能被当成 user@host)。
	if i := strings.Index(s, "@"); i > 0 && i < len(s)-1 && !strings.Contains(s[:i], "/") {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	// host 小写(路径部分**不动**:仓库路径大小写敏感,小写化会指向不存在的仓库)
	if i := strings.Index(s, "/"); i > 0 {
		return strings.ToLower(s[:i]) + s[i:]
	}
	return strings.ToLower(s)
}

// SplitSpec 拆 `<repo>[@<ref>]`(ref 空 = 未指定 = 默认分支)。
//
// 注意用 **LastIndex("@")** 而不是 Index:`git@github.com:a/b` 里那个 @ 不是 ref 分隔符。
// 已有代码也是 LastIndex,但只在 `i > 0` 时拆 —— 这里保持一致并额外挡掉 ref 为空的情况。
func SplitSpec(spec string) (repo, ref string) {
	i := strings.LastIndex(spec, "@")
	if i > 0 && i < len(spec)-1 {
		return spec[:i], spec[i+1:]
	}
	return spec, ""
}

// IsCommitSHA 40 位 hex(也可接受 7-40 位?—— 不行:短 sha 在不同 clone 下可能指不同对象,
// 且 `--branch` 认不了;统一要求完整 40 位,判错文案见 §1.4)。
func IsCommitSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// ClassifyRef 由 ref 的形状判定来源种类(**不看远端**,零网络)。
//
// `git clone --branch` 分不清 branch 与 tag ⇒ 无 `@ref` 时是 default,带 `@ref` 时形态上
// 只能判成 tag —— 这个诚实(而不是假装分得清)是刻意的:判错的后果是"拦了一个 branch"
// 或"放行了一个被 force-push 的 tag",后者才是真问题。因此**tag 判定刻意保守**:
// 装完读回真实种类(resolveKind)会尝试用 `git ls-remote` 区分,并把结果写回账。
func ClassifyRef(ref string) string {
	switch {
	case ref == "":
		return KindDefault
	case IsCommitSHA(ref):
		return KindCommit
	default:
		return KindTag
	}
}

// DriftVerdict 漂移判定的结果。
type DriftVerdict struct {
	// Reject true = 拒绝安装(tag 被改了)。此时 Err 非空,文案带两条出路。
	Reject bool
	// Drifted true = 移动引用变了(branch/default)⇒ 放行但记账。
	Drifted bool
	// From/To 旧 sha 与新 sha(短显示由调用方截断)。
	From, To string
	// Err 拒绝时的可执行文案。
	Err error
}

// CheckDrift 按 §1.3 的表判定。
//
// 为什么**拦 tag 不拦 branch**:tag 是作者「这一版就是这一版」的承诺,被重推属于违反承诺;
// 而拦 branch 等于逼所有人打 tag —— 长尾作者很少打,结果是逼所有人只能装一个会动的引用。
//
// kind=commit 不可能漂:用户指名了 40 位 sha,解析到别的对象才是 bug(见 cloneBySHA 的
// fetch+checkout 失败即报错,不静默换对象)。
func CheckDrift(prev SourceEntry, newRef, resolvedSHA string, acceptDrift bool) DriftVerdict {
	if prev.Repo == "" || resolvedSHA == "" || prev.Commit == "" {
		return DriftVerdict{} // 没有可比的东西(首装 / 解析失败)⇒ 无判定
	}
	if resolvedSHA == prev.Commit {
		return DriftVerdict{} // 幂等重装
	}
	v := DriftVerdict{From: prev.Commit, To: resolvedSHA}
	switch prev.Kind {
	case KindTag:
		if acceptDrift {
			v.Drifted = true
			return v
		}
		v.Reject = true
		v.Err = fmt.Errorf("install: 拒绝安装 %s:%s —— 这个 tag 现在指向 %s,但你上次装的是 %s。"+
			"tag 被改动有两种可能:作者重推了这一版,或仓库/账号易手(同名仓库被别人接手)。"+
			"确认要换成新版:加 --accept-drift 重跑;要装回原来那版:改成 @%s",
			prev.Repo, prev.Ref, shortSHA(resolvedSHA), shortSHA(prev.Commit), prev.Commit)
		return v
	default: // branch / default / local
		v.Drifted = true
		return v
	}
}

// shortSHA sha 短显示(12 位;面板与文案用)。不足 12 位原样返回。
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// ShortSHA 导出版(sources 是同包,给 web/CLI 显示用)。
func ShortSHA(sha string) string { return shortSHA(sha) }

// nowRFC 统一的时间戳格式(便于测试断言)。
func nowRFC() string { return time.Now().UTC().Format(time.RFC3339) }

// MarkOfficial 把官方随附的外部插件在来源账里标成 `official`(批四 §P3)。
//
// **为什么由 cmd/gah 调而不是 internal/embed 写**:internal/install 的**内部测试**要 import
// internal/embed(用 EnsureSeed 造 home),而 internal/embed 若反过来 import internal/install
// 就成环(Go 不允许包与它的内部测试互相 import)。cmd/gah 是唯一同时装配两侧的地方,
// 而"官方插件由它释放"这件事本来也只有它知道。
//
// 幂等,且**不覆盖已有条目**:官方插件没有 repo/ref,只需要 origin 这一维;
// 真正的内容(哈希)在 SHA256SUMS 里,那里才是"盘上这份有没有被换"的权威。
func MarkOfficial(home string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	ledger, err := LoadSources(home)
	if err != nil {
		return err
	}
	changed := false
	for _, n := range names {
		if e, ok := ledger.Find(n); ok {
			if e.Origin == OriginOfficial {
				continue
			}
			// 已被用户装的同名插件占位 ⇒ 不抢。
			_ = e
			continue
		}
		ledger.Record(SourceEntry{
			PluginID: n, Kind: "official", Origin: OriginOfficial,
			InstalledAt: nowRFC(),
		})
		changed = true
	}
	if !changed {
		return nil
	}
	return WriteSources(home, ledger)
}
