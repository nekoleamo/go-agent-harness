// Package install 提供插件安装/卸载/清单(M6.6,gah -install/-uninstall/-list-plugins)。
// 流程:拉取(桥协议:git clone → 构建 → 落 ~/.gah/plugins/<id>/)→ 登记(enabled
// patch 幂等合并)→ 装配(home profile 引用 patch,装完即启用)。卸载:删目录
// (host-bridge watch 自动撤销工具)。MCP 插件:直接生成 mcp-bridge 配置条目。
// manifest(仓库根 plugin.yaml):{id, protocol: bridge|mcp, binary, build}。
package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// PatchFile 安装登记的 patch 文件名(profile 引用)。
const PatchFile = "patch-installed.yaml"

// Manifest 插件仓库声明(plugin.yaml)。
type Manifest struct {
	ID       string `yaml:"id"`
	Protocol string `yaml:"protocol"` // bridge | mcp(仓库侧一般 bridge)
	Binary   string `yaml:"binary"`   // 桥插件产物文件名(须 tool- 前缀)
	Build    string `yaml:"build"`    // 构建命令(默认 go build -o <binary> .)
	// APIVersion 插件协议版本(2026-10-03 加)。**缺省 = 视为 v1**(兼容既有插件,
	// 不能因为加了一个字段就让已发布的插件全被拒);写了但认不出来 ⇒ 显式拒绝。
	//
	// 为什么需要它:插件是**常驻进程**,宿主升级后协议可能变(工具定义字段、回调通道、
	// 沙箱握手)。没有版本闸的表现是「装得上、起得来、跑到一半静默不对」——
	// 比装不上更难查。版本号让不兼容在安装那一刻就说清。
	//
	// 形状从**单值**改成**兼容范围**(批一 §1.6):宿主支持多个协议版本时,单值会要求
	// 插件作者在每次宿主升级时改 manifest(一个对插件作者纯负担的字段)。
	// 写法:`api_version: v1` 或 `api_version: [v1, v2]`(列表 = 声明「我支持这几版」)。
	// 注意**不**给 sdk.Capabilities 加这个字段 —— 那是给插件作者加负担;
	// 本地兼容性提示走来源账(装机时宿主自己写下的),见 CompatibilityNotice。
	APIVersion StringList `yaml:"api_version,omitempty"`
	// Prebuilt 作者发布的**预编译产物**下载表(批三),键 = `<goos>/<goarch>`。
	//
	// 为什么放在 manifest 里而不是另建一个索引服务:索引服务意味着"还要信任另一个人",
	// 而发布页(Release)本来就是作者发东西的地方。
	//
	// **写了才允许 --prebuilt**:不写就报错,绝不悄悄回退到源码构建(那要跑仓库里的任意 shell,
	// 正是这个开关要避开的事)。
	Prebuilt map[string]string `yaml:"prebuilt,omitempty"`
}

// StringList yaml 里「单值或列表」都能写的字符串字段。
//
// 为什么要有它:`api_version: v1` 与 `api_version: [v1, v2]` 是同一件事的两种写法,
// 而 yaml.v3 默认只认其中一种。用自定义类型收下两种,免得作者写错一个就被静默忽略
// (那等于「声明了版本但宿主当没看见」,正是这个字段要防的事)。
type StringList []string

// UnmarshalYAML 接受单值字符串或字符串序列。
func (s *StringList) UnmarshalYAML(unmarshal func(any) error) error {
	var single string
	if err := unmarshal(&single); err == nil {
		if strings.TrimSpace(single) == "" {
			*s = nil
		} else {
			*s = StringList{single}
		}
		return nil
	}
	var many []string
	if err := unmarshal(&many); err != nil {
		return err
	}
	*s = many
	return nil
}

// Strings 归一后的取值(去空白、去空项)。
func (s StringList) Strings() []string {
	var out []string
	for _, v := range s {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Any 任意一项非空(用于「声明了吗」)。
func (s StringList) Any() bool { return len(s.Strings()) > 0 }

// PluginAPIVersionSupported 宿主支持的插件协议版本范围(安装兼容闸的权威)。
//
// 形如 `v1` / `v1..v2` 的范围串是**故意**没做的:协议版本号是单调的字符串,而「范围」要用
// 区间还是集合取决于协议演进史,现在定死一个语义将来很难改。一张**集合**足够,且集合的
// 语义不会因为以后加版本而改变含义。
var PluginAPIVersionSupported = []string{"v1"}

// checkAPIVersion 兼容闸:缺省放行(视作 v1);声明里至少一版命中宿主范围则放行;
// 一个都没命中 ⇒ 显式报错并说清宿主支持什么。
//
// 「兼容范围」的判定方向很重要:插件声明它**支持哪几版**(不是我需要哪版),
// 于是宿主是「范围 ∩ 宿主支持集 ≠ 空」。反过来问(宿主需要的那版有没有被声明)会把
// 「插件只声明了未来版本」也放过去 —— 而那正是协议不兼容的情形。
func checkAPIVersion(man Manifest) error {
	declared := man.APIVersion.Strings()
	if len(declared) == 0 {
		return nil
	}
	for _, d := range declared {
		if supportsAPIVersion(d) {
			return nil
		}
	}
	return fmt.Errorf("install: plugin.yaml 声明 api_version=%s,本版 gah 只支持 %s"+
		"(插件协议变了:请用与本版 gah 匹配的插件版本,或升级 gah)", joinAPIVersions(declared), joinAPIVersions(PluginAPIVersionSupported))
}

func supportsAPIVersion(v string) bool {
	for _, s := range PluginAPIVersionSupported {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	// 裸数字视为该版本的 v 形式等价(`api_version: 1` 与 `api_version: v1`)。
	if !strings.HasPrefix(v, "v") {
		for _, s := range PluginAPIVersionSupported {
			if strings.EqualFold(strings.TrimPrefix(s, "v"), v) {
				return true
			}
		}
	}
	return false
}

func joinAPIVersions(v []string) string {
	if len(v) == 0 {
		return "(未声明)"
	}
	return strings.Join(v, ", ")
}

// IncompatibleSource 一条「装机时声明的协议版本不在本版 gah 范围内」的记录。
type IncompatibleSource struct {
	PluginID string
	// Declared 插件声明的版本(逗号分隔;来源账里存的是这个形状)。
	Declared string
}

// CompatibilityNotice 本地兼容性提示(§1.6):扫来源账,找出声明了本版 gah 不认的协议版本的插件。
//
// 为什么是「提示」而不是「拒绝加载」:**装的时候已经过了兼容闸**,会走到这一步说明
// 用户升了 gah 而没升插件(或者手工改了账)。启动时把一个此前能跑的插件直接踢掉,
// 后果比"少一个工具"严重得多(它会让用户在排查前先失去手边能用的东西)。
// 把事实摆出来、让用户自己决定,才是这里该做的。
//
// 零网络:只看来源账里装机时写下的字符串,不发一个请求。
func CompatibilityNotice(home string) ([]IncompatibleSource, error) {
	ledger, err := LoadSources(home)
	if err != nil {
		return nil, err
	}
	var out []IncompatibleSource
	for _, e := range ledger.All() {
		if strings.TrimSpace(e.APIVersion) == "" {
			continue // 未声明 = 视作 v1,不必报
		}
		if APIVersionSupported(e.APIVersion) {
			continue
		}
		out = append(out, IncompatibleSource{PluginID: e.PluginID, Declared: e.APIVersion})
	}
	return out, nil
}

// APIVersionSupported 某条装机时记下的声明版本串是否与本版 gah 有交集(逗号分隔的列表)。
//
// 包内的 supportsAPIVersion 是单值判定(不导出);这里给面板一个列表版的壳,
// 免得 web 侧自己拆逗号再逐个判 —— 那样同一个口径会有两份实现。
func APIVersionSupported(declared string) bool {
	for _, d := range strings.Split(declared, ",") {
		if supportsAPIVersion(strings.TrimSpace(d)) {
			return true
		}
	}
	return false
}

// CompatibilityNoticeText 把兼容性问题汇成一句面板可展示的话(无问题 ⇒ 空串)。
//
// 汇总而不逐条列:面板顶部只该有一条横幅;逐条列会把"插件越多越吵"这个反馈做出来,
// 而那会训练用户无视它。
func CompatibilityNoticeText(home string) string {
	bad, err := CompatibilityNotice(home)
	if err != nil || len(bad) == 0 {
		return ""
	}
	var names []string
	for _, b := range bad {
		names = append(names, fmt.Sprintf("%s(声明 %s)", b.PluginID, b.Declared))
	}
	return fmt.Sprintf("%d 个插件声明的协议版本不在本版 gah 的支持范围 %s 内:%s。"+
		"它们仍然在运行,但可能出现工具缺失或参数对不上 —— 重装或升级这些插件可解决。",
		len(bad), joinAPIVersions(PluginAPIVersionSupported), strings.Join(names, "、"))
}

// Result 安装结果摘要。
type Result struct {
	ID       string
	Protocol string
	Binary   string
	Dir      string
	Patch    string
	// Source 实际来源(repo spec 或本地绝对路径),原样回显给用户核对。
	Source string
	// BuildCmd 实际执行的构建命令(plugin.yaml 的 build,空 = 用默认)。
	// 为什么要往外送:确认文案必须含**命令原文**(它可以是任意 shell 命令)——
	// 由内核算好给入口,三个入口就不会拼出三种不一样的说法。
	BuildCmd string
	// Local 是不是从本地目录装的(本地目录的 id 默认是路径,展示上要说清来源)。
	Local bool
	// Tidied 构建前是否补跑过 go mod tidy(⇒ 插件引入了仓库原本没声明的模块依赖)。
	Tidied bool
	// Audit 这次登记进白名单的审计行(时间/来源/哈希);装完回显给用户看。
	Audit plugintrust.AuditEntry
	// Drifted 本次安装解析到的 sha 与上次不同(且是移动引用)⇒ 必须醒目提示。
	// 为什么不塞进 Err:它是**正常安装成功**,只是来源变了;让用户以为失败反而会促使
	// 他去加 --accept-drift 绕过 tag 守卫。
	Drifted bool
	// DriftFrom/DriftTo 漂移前后的 sha(短显示)。
	DriftFrom, DriftTo string
	// Record 这次写进来源账的条目(面板显示来源用)。
	Record SourceEntry
	// BuildImplicit 未声明 `build:` ⇒ 用的是**我们替作者选的**默认命令(批六 §6.1)。
	// 为什么要往外送:三个入口都必须标注「这条命令不是你仓库里写的」,否则用户看到
	// 一条自己从没写过的命令被执行,却以为是仓库的要求。
	BuildImplicit bool
	// Artifact true = 这次是「直接安装已构建好的产物」(-install-artifact),
	// 既不是源码构建也不是 manifest 声明的预编译路径。
	Artifact bool
	// Prebuilt 非空 = 产物是下载来的,值是它的 URL(预编译路与产物安装路共用这一维)。
	// 为什么要单独送出来:「产物是下载来的,不是你机器上构建的」是**信任语义**的事实,
	// 回执必须显式说出来,不能只靠 BuildCmd 里那句括号。
	Prebuilt string
}

// DriftWarning 漂移提示文案(移动引用换了 sha 时必说;空串 = 无漂移)。
func (r *Result) DriftWarning() string {
	if r == nil || !r.Drifted {
		return ""
	}
	kind := r.Record.Kind
	if kind == "" {
		kind = KindDefault
	}
	return fmt.Sprintf("注意:%s 是个会移动的引用(%s),这次解析到的 sha 与上次不同(%s → %s)。"+
		"这是正常的(作者推了新 commit),但「重跑同一条命令就装到不同代码」这件事你应该知道。",
		r.Record.Ref, kind, shortSHA(r.DriftFrom), shortSHA(r.DriftTo))
}

// Facts 把结果整成确认文案所需的事实(入口在**执行前**用 SpecFacts)。
func (r *Result) Facts() ConfirmFacts {
	return ConfirmFacts{Source: r.Source, ID: r.ID, Dir: r.Dir, BuildCmd: r.BuildCmd}
}

// Install 安装插件:spec 形如 <repo>[@<version>](桥)或 mcp:<id>:<command>(MCP)。
func Install(spec, home string) (*Result, error) { return InstallWithOpts(spec, home, InstallOpts{}) }

// InstallOpts 安装开关。
//
// 为什么是一组开关而不是签名上堆参数:这三项都只在**异常路径**上起作用(接受漂移/预编译/
// 显式构建),而绝大多数调用方一个都不用 —— 签名上堆三个 bool 会让每个调用点都得写三个 false。
type InstallOpts struct {
	// AcceptDrift 接受 tag 漂移(§1.3)。默认 false = 拒绝并给两条出路。
	AcceptDrift bool
	// Prebuilt 走 plugin.yaml 的 prebuilt: 段下载产物,完全跳过本地构建(批三)。
	Prebuilt bool
	// AllowImplicitBuild 允许 `build:` 未声明时走默认构建命令(批六 §6.1 的 C 留口;
	// 见 build.go 的 implicitBuildAllowed —— 当前 A 口径生效,这个字段只作上限)。
	AllowImplicitBuild bool
}

// InstallWithOpts 带开关的安装。
func InstallWithOpts(spec, home string, opts InstallOpts) (*Result, error) {
	if strings.HasPrefix(spec, "mcp:") {
		return installMCP(spec, home)
	}
	return installBridge(spec, home, opts)
}

// installBridge 拉取或就地读取 + 构建 + 落目录 + 登记。
//
// 来源两类(判据 isLocalDir,与 install-ui 同款 —— 同一个词在一个项目里必须是同一个意思):
//   - **本地目录**:`/abs/path`、`./rel`、`../rel`、或任何已存在的路径。**自己写的插件
//     不该被迫先 git init + push** —— 那是纯仪式,而且会把源码推到某个远端。
//   - git repo:`<repo>[@<version>]`(http(s)/git@/.git 一律按 repo 处理)。
//
// 本地目录**复制到临时区再构建**:构建产物必须落在我们自己管理的目录里
// (`go build` 会在源目录留 cache/临时文件,而用户的源码目录不该被构建过程弄脏)。
func installBridge(spec, home string, opts InstallOpts) (*Result, error) {
	tmp, err := os.MkdirTemp("", "gah-install-")
	if err != nil {
		return nil, err
	}
	defer sdk.RemoveTree(tmp)
	clone := filepath.Join(tmp, "repo")

	local := isLocalDir(spec)
	source := spec // 回显用:本地目录先按用户给的原样,末尾再换成绝对路径
	// 来源账的三件:归一化后的仓库名、ref、以及 ref 的种类(空 = 后面按形态判定)。
	var (
		normRepo  string
		ref       string
		kind      string
		localPath string
	)
	if local {
		src, err := filepath.Abs(spec)
		if err != nil {
			return nil, fmt.Errorf("install: 解析本地路径 %s: %w", spec, err)
		}
		if !fileExists(src) {
			return nil, fmt.Errorf("install: 本地路径不存在: %s", src)
		}
		if err := copyDir(src, clone); err != nil {
			return nil, fmt.Errorf("install: 复制本地目录: %w", err)
		}
		source = src // 绝对路径比用户手打的相对路径更可核对
		localPath, kind = src, KindLocal
	} else {
		repo, r := SplitSpec(spec)
		ref = r
		kind = ClassifyRef(ref)
		normRepo = NormalizeRepo(repo)
		if err := cloneRepo(repo, ref, clone); err != nil {
			return nil, err
		}
		// 区分 branch 与 tag:`git clone --branch` 不区分,只能问远端一次。
		// 判错的代价不对称 —— 把 branch 当 tag 会**误拒**合法的移动引用(用户会以为坏了),
		// 所以宁可多一次网络往返也不能省。问不动(离线/权限)则按形态保守判,见 resolveKind。
		kind = resolveKind(repo, ref, clone, kind)
	}
	man := readManifest(clone)
	if man.ID == "" {
		return nil, fmt.Errorf("install: 仓库缺少 plugin.yaml(id 必填)")
	}
	// 漂移判定(§1.3)放在**拉取之后、构建之前**:① 要 plugin.yaml 的 id 才知道账上那条是谁;
	// ② 但绝不能更晚 —— 此时还没往 home 写任何东西,拒绝就是真的没装。
	sha := resolveCommit(clone)
	ledger, err := LoadSources(home)
	if err != nil {
		return nil, err // 账坏了要报出来(不能当成首装覆盖掉旧记录,见 LoadSources)
	}
	var verdict DriftVerdict
	if local {
		// 本地目录没有远端可比,但**本地插件也可以被 git 管着**。仍走同一套判定:
		// 记录的 kind 若是 commit/tag 却在本地复制里无从比较 ⇒ 无判定(自然落到零值)。
		verdict = CheckDrift(ledger.MustFind(man.ID), "", sha, opts.AcceptDrift)
	} else {
		verdict = CheckDrift(ledger.MustFind(man.ID), ref, sha, opts.AcceptDrift)
	}
	if verdict.Reject {
		return nil, verdict.Err
	}
	// 兼容闸同样在拉取之后、构建之前:不该为一个注定装不上的版本烧一次 go build。
	if err := checkAPIVersion(man); err != nil {
		return nil, err
	}
	if man.Protocol != "" && man.Protocol != "bridge" {
		return nil, fmt.Errorf("install: 仓库声明 protocol=%s,仅支持 bridge", man.Protocol)
	}
	binary := man.Binary
	if binary == "" {
		binary = "tool-" + man.ID
	}
	if !strings.HasPrefix(binary, "tool-") {
		return nil, fmt.Errorf("install: 二进制名须 tool- 前缀(host-bridge 扫描约定): %s", binary)
	}
	// Windows 汇总成 .exe:源码路的 `go build -o <名> .` 与预编译路都按这个名字落盘,
	// 而 os/exec 对绝对路径不做 PATHEXT 补全 ⇒ 不带 .exe 就是「装上了、重启后不加载」
	// (2026-10-10 查 Go 源码确认:`-o` 非目录时不补扩展名,两条路都得补;见 sdk.BinaryName)。
	binary = sdk.BinaryName(binary)
	// built2.Cmd / built2.Tidied 两条回执字段在两条路上都有意义:
	// 源码路是「跑了哪条命令 / 补没补依赖」;预编译路是「没跑构建 / 从哪个 URL 下的」。
	buildCmd := strings.TrimSpace(man.Build)
	// buildImplicit:未声明 `build:` ⇒ 用的是**我们替作者选的**默认命令(批六 §6.1 的 A 口径)。
	buildImplicit := false
	built := filepath.Join(clone, binary)
	var built2 buildResult
	prebuiltURL := ""
	if opts.Prebuilt {
		// 预编译路:**一个 shell 都不跑**。本机可以没有 go / node / make。
		// 找不到当前平台的条目时 fetchPrebuilt 返回**显式错误**而**不**回退到源码构建 ——
		// 用户点了这个开关就是为了不跑陌生脚本,悄悄回退等于把它变成谎言。
		url, err := man.prebuiltDownloadURL()
		if err != nil {
			return nil, err
		}
		if err := downloadAndVerify(url, built); err != nil {
			return nil, err
		}
		prebuiltURL = url
		built2 = buildResult{Cmd: "(未执行构建:走 --prebuilt,产物由作者发布于 " + url + ")"}
	} else {
		if buildCmd == "" {
			buildImplicit = true
			// **C 留口的判定点**(批六 §6.1):这里就是「隐式构建」该不该被拦的那一行。
			// A 口径现在是「照常装 + 三处标注」;若将来要改成 C(拒绝),就在这里返回错误。
			//
			// 为什么现在不拦:全仓**没有任何 plugin.yaml**(官方外部插件走
			// gen-extplugins.sh 直接 go build,从不经过 install.Install)⇒ 对本仓零影响;
			// 而长尾第三方插件绝大多数不会写 `build:`,一刀切拒绝等于把安装这条路整体堵死。
			// 只有 C 口径**已启用**且本次没显式放行时才拒。当前 implicitBuildAllowed()
			// 恒 false ⇒ 这条永不触发,A 口径(照常装 + 标注)是默认。
			if implicitBuildAllowed() && !opts.AllowImplicitBuild {
				return nil, fmt.Errorf("install: %s 的 plugin.yaml 没有声明 build:,而隐式构建已被拒绝。"+
					"要按默认构建(go build -o %s .)装它:加 %s=1 重跑,或让作者在 plugin.yaml 里写明 build",
					man.ID, binary, AllowImplicitPluginBuildEnv)
			}
			buildCmd = defaultBuildCmd(binary) // -mod=readonly;tidy 只在需要时补跑(见 build.go)
		}
		built2, err = buildPlugin(clone, buildCmd, binary)
		if err != nil {
			return nil, err
		}
		// 补过依赖这件事**必须回显**:它意味着这个插件往 go.mod 里新增了仓库原本没声明的
		// 模块 —— 供应链面被装插件这件事扩宽了,用户有权知道(见 build.go 的 ①)。
		if built2.Tidied {
			fmt.Fprintf(os.Stderr, "gah: 安装 %s:仓库的 go.mod 不完整,已补跑 go mod tidy(引入了仓库未声明的模块依赖)\n", man.ID)
		}
	}
	if _, err := os.Stat(built); err != nil {
		return nil, fmt.Errorf("install: 构建产物缺失 %s: %v", binary, err)
	}
	// 落 ~/.gah/plugins/<id>/
	dir := filepath.Join(home, "plugins", man.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := copyFile(built, filepath.Join(dir, binary)); err != nil {
		return nil, fmt.Errorf("install: 复制产物: %w", err)
	}
	if mf := filepath.Join(clone, "plugin.yaml"); fileExists(mf) {
		_ = copyFile(mf, filepath.Join(dir, "plugin.yaml"))
	}
	// 登记白名单(2026-10-03):白名单一存在即强制,装完不登记 ⇒ 下次启动被自己拒掉。
	// 登记的是**刚构建出来的这份**的哈希,不是发布方声明的值 —— 我们只能担保自己
	// 装进去的这份,担保不了「作者那一份本来就好」。
	sum, err := plugintrust.HashFile(filepath.Join(dir, binary))
	if err != nil {
		return nil, fmt.Errorf("install: 算产物哈希失败: %w", err)
	}
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		return nil, err
	}
	// 审计来源标 `install-prebuilt:<url>`:预编译路与源码路的信任语义**不同** ——
	// 前者的产物是**下载来的**,不是在你机器上构建的,事后必须能一眼分开。
	auditSource := "install:" + spec
	if opts.Prebuilt {
		auditSource = "install-prebuilt:" + prebuiltURL
	}
	if err := list.RecordWithAudit(binary, sum, auditSource); err != nil {
		return nil, fmt.Errorf("install: 登记 plugins/%s 失败: %w", plugintrust.FileName, err)
	}
	// 登记:host-bridge 指向 home/plugins
	patch := filepath.Join(home, "config", PatchFile)
	if err := EnsurePatch(patch, Entry{
		ID: "host-bridge", Enabled: true,
		Data: map[string]any{"dir": filepath.Join(home, "plugins"), "watch": true},
	}); err != nil {
		return nil, err
	}
	// 装配:profile 引用 patch(幂等,装完即启用)
	if err := EnsureProfilePicks(home); err != nil {
		return nil, err
	}
	audit, _ := list.LastAuditOf(binary)
	// 写来源账(在白名单之后):两份账的**因果**是这个顺序 —— 白名单回答「盘上这份没被换」,
	// 来源账回答「当初装的是哪一份」。两者都写成功才算一次完整安装。
	entry := SourceEntry{
		Repo: normRepo, Ref: ref, Kind: kind, Commit: sha,
		PluginID: man.ID, APIVersion: strings.Join(man.APIVersion.Strings(), ","),
		Origin: OriginUser, LocalPath: localPath, Prebuilt: prebuiltURL,
		InstalledAt: nowRFC(), Drifted: verdict.Drifted,
	}
	if entry.Kind == "" {
		entry.Kind = KindLocal
	}
	if normRepo == "" {
		// 本地目录:把绝对路径当「仓库」记(它就是一个可定位的来源),并降为 local。
		entry.Repo = localPath
	}
	if err := writeLedger(home, ledger, entry); err != nil {
		return nil, fmt.Errorf("install: 写 %s 失败(插件已装上,但来源账没写成 —— 再装一次会覆盖旧记录): %w", SourcesFile, err)
	}
	res := &Result{
		ID: man.ID, Protocol: "bridge", Binary: binary, Dir: dir, Patch: patch,
		Source: source, BuildCmd: built2.Cmd, Local: local, Tidied: built2.Tidied, Audit: audit,
		Drifted: verdict.Drifted, DriftFrom: verdict.From, DriftTo: verdict.To, Record: entry,
		Prebuilt: prebuiltURL,
	}
	if buildImplicit {
		res.BuildImplicit = true
		// 未声明 `build:` ⇒ 三处都要标注「这条命令是默认的,不是你仓库里写的」。
		res.BuildCmd += "(plugin.yaml 未声明 build,用的是默认构建命令)"
	}
	return res, nil
}

// writeLedger 记一条并落盘(测试可替掉时间戳来源)。
func writeLedger(home string, ledger *SourceLedger, e SourceEntry) error {
	if ledger == nil {
		ledger, _ = LoadSources(home)
	}
	ledger.Record(e)
	return WriteSources(home, ledger)
}

// installMCP 直接生成 mcp-bridge 配置条目(无需拉取)。
func installMCP(spec, home string) (*Result, error) {
	rest := strings.TrimPrefix(spec, "mcp:")
	i := strings.Index(rest, ":")
	if i <= 0 {
		return nil, fmt.Errorf("install: mcp 格式为 mcp:<id>:<command>,收到 %q", spec)
	}
	id, cmd := rest[:i], rest[i+1:]
	if strings.TrimSpace(cmd) == "" {
		return nil, fmt.Errorf("install: mcp 需要启动命令")
	}
	patch := filepath.Join(home, "config", PatchFile)
	if err := EnsurePatch(patch, Entry{
		ID: "mcp-bridge", Enabled: true,
		Data: map[string]any{"command": cmd},
	}); err != nil {
		return nil, err
	}
	if err := EnsureProfilePicks(home); err != nil {
		return nil, err
	}
	return &Result{ID: id, Protocol: "mcp", Patch: patch}, nil
}

// Uninstall 卸载:停进程 → 删目录 → 撤白名单 → 撤来源账(批二 §2.7)。
//
// **顺序是承重的**。①ctl.Disable 必须在 ②os.RemoveAll 之前:反过来就回到
// 「用户删了文件,进程却继续跑到 gah 重启」—— 运行中的进程持着工具注册与回调 token,
// 而用户已经认为它没了。**那给的是虚假的安全感。**
//
// ctl 可以为 nil:`gah -uninstall <id>` 是 CLI 场景,gah 进程本身没在跑(装了插件但没起
// 服务),没有活进程可停 —— 这时 ① 天然无事可做,不是错误。
//
// 顺带把「停用 ≠ 卸载」的区别摆在这里:停用(Disable)留文件与两个条目;本函数删全部。
func Uninstall(id, home string, ctl sdk.ExternalPlugins) error {
	plugins := filepath.Join(home, "plugins")
	dir := filepath.Join(plugins, id)
	if !fileExists(dir) {
		return fmt.Errorf("uninstall: 插件 %s 未安装(%s)", id, dir)
	}
	// 白名单的键是**二进制文件名**(tool-demo / tool-kit.exe),不是插件 id(demo) ——
	// 必须在删目录**之前**把它读出来:删完就无从得知该撤哪一条了。
	// 撤不掉的后果很具体:同名再装、构建产物哈希与上次不同时,会被**自己的旧条目**拒掉,
	// 而错误文案说的是「文件可能已被改动」,与真实原因毫无关系。
	// ① 先停进程:停**该目录下的全部**二进制(一个插件目录里可能有多个 tool-*)。
	// 用 ctl.List 拿路径而不是猜文件名 —— 猜错了就停不掉,而停不掉正是要防的那件事。
	// 停不掉就**中止**(不删文件):报错让人重试,好过删完再告诉用户停不掉。
	if ctl != nil {
		for _, info := range ctl.List() {
			if info.Path == "" || !underDir(info.Path, dir) {
				continue
			}
			if err := ctl.Disable(info.Name); err != nil {
				return fmt.Errorf("uninstall: 停不掉 %s 的进程,已中止卸载(文件未删): %w", info.Name, err)
			}
		}
	}
	bins := pluginBinNames(dir)
	if err := sdk.RemoveTree(dir); err != nil {
		return err
	}
	list, err := plugintrust.Load(plugins)
	if err != nil {
		return err // 清单坏了要报出来,不能悄悄留着幽灵条目
	}
	for _, b := range bins {
		if err := list.Remove(b); err != nil {
			return err
		}
	}
	// 撤来源账(与白名单同理由:条目留着就等于给一个不存在的插件编了一段历史)。
	// 同样**在删目录前**做不了(要先知道 id,而 id 就是目录名)—— 这里 id 是参数,天然有。
	ledger, err := LoadSources(home)
	if err != nil {
		return err
	}
	if _, ok := ledger.Find(id); ok {
		ledger.Remove(id)
		if err := WriteSources(home, ledger); err != nil {
			return err
		}
	}
	return nil
}

// underDir path 是否在 dir 之下(含 dir 本身)。
//
// 为什么需要它:卸载时要停掉的是「这个插件目录里的二进制」,而控制面给的是绝对路径。
// 不判包含关系而只比目录名,会把另一个插件的进程也停掉(或者反过来一个都没停到)。
func underDir(path, dir string) bool {
	ap, err := filepath.Abs(path)
	if err != nil {
		ap = filepath.Clean(path)
	}
	ad, err := filepath.Abs(dir)
	if err != nil {
		ad = filepath.Clean(dir)
	}
	return ap == ad || strings.HasPrefix(ap, ad+string(filepath.Separator))
}

// ListBinaryName List 条目的二进制名(带回落),供 CLI 清单用。
//
// 为什么要它:Item.Binary 只来自 plugin.yaml,而**官方件与手工放置**的插件根本没写
// manifest ⇒ 那一栏是空的。用户看到「空的一栏」无法区分「这个插件没有二进制」
// 与「我没写 manifest」—— 与 web / `/install list` 显示**空值**是同一件事,而那两处
// 早就改用回落了(见 BinaryName 的注释)。三面口径必须一致,否则 CLI 成了唯一
// 说不清的那个。
func ListBinaryName(it Item) string {
	if strings.TrimSpace(it.Binary) != "" {
		return it.Binary
	}
	return BinaryName(it.Dir)
}

// BinaryName 插件目录里**实际会加载的那个二进制**的文件名。
//
// 为什么需要它:`Item.Binary` 只来自 plugin.yaml,而**手工放置**的插件(没写 manifest)
// 那一栏是空的 —— 面板与 /install 清单若直接显示空值,用户看到的是"这个插件没有二进制",
// 与"我没 manifest"完全两回事。故回落到扫目录(与 host-bridge 的 isExternalPluginBin
// 同一口径:`tool-` / `cmd-` 前缀),找不到返回 ""。
func BinaryName(dir string) string {
	names := pluginBinNames(dir)
	if len(names) == 0 {
		return ""
	}
	// manifest 声明的名字也要过平台扩展名归一:Windows 上盘上是 tool-x.exe,
	// 而 plugin.yaml 里写的是 tool-x —— 直接判 fileExists 会失配,回落到扫描第一个又未必是它。
	if m := readManifest(dir); m.Binary != "" {
		if b := sdk.BinaryName(m.Binary); fileExists(filepath.Join(dir, b)) {
			return b
		}
	}
	return names[0]
}

// pluginBinNames 目录里会被 host-bridge 当成插件的二进制名(manifest 声明优先,
// 否则扫 tool-*/cmd-*;删目录前调用)。
func pluginBinNames(dir string) []string {
	var out []string
	if m := readManifest(dir); m.Binary != "" {
		out = append(out, sdk.BinaryName(m.Binary)) // 平台扩展名归一(见 BinaryName 注释)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), "tool-") || strings.HasPrefix(e.Name(), "cmd-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// Item 已安装插件条目。
type Item struct {
	ID       string
	Protocol string
	Binary   string
	Dir      string
	Manifest string
}

// List 列出已安装插件(home/plugins 下一层目录)。
func List(home string) []Item {
	var out []Item
	root := filepath.Join(home, "plugins")
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		item := Item{ID: e.Name(), Dir: dir}
		if mf := filepath.Join(dir, "plugin.yaml"); fileExists(mf) {
			item.Manifest = mf
			b, _ := os.ReadFile(mf)
			var m Manifest
			if yaml.Unmarshal(b, &m) == nil {
				item.Protocol = m.Protocol
				item.Binary = m.Binary
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// readManifest 读仓库 plugin.yaml(缺省返回空结构)。
func readManifest(dir string) Manifest {
	b, err := os.ReadFile(filepath.Join(dir, "plugin.yaml"))
	if err != nil {
		return Manifest{Protocol: "bridge"}
	}
	var m Manifest
	if yaml.Unmarshal(b, &m) != nil {
		return Manifest{Protocol: "bridge"}
	}
	return m
}

// run 执行命令并回传输出。
func run(dir, name string, args ...string) (string, error) {
	return runEnv(dir, nil, name, args...)
}

// runEnv 同 run,但可指定子进程环境(env = nil 时继承宿主环境)。
// 拆分只为让「清洗后的环境」有唯一入口,免得各处各写一遍 exec.Command。
func runEnv(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
