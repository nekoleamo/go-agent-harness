// Package kernelsandbox 提供**进程级内核沙箱**(macOS seatbelt / Linux Landlock)。
//
// 定位:这是"工具边界协作式控制"之外的最后一层 —— 协作层只看得到命令文本/工具参数里的写目标,
// 进程内部自选的写(解释器里动态拼路径、构建工具自选落点、第三方 MCP server / 插件进程)
// 只有内核层拦得住。
//
// 为何单独一个包(而不是留在 tool-shell):2026-09-27 安全审计发现**外部进程的写完全在裁决面之外**
// (`mcp-bridge` 起的 MCP server、`host-bridge` 起的外部插件都是 raw `exec.Command`),
// 而 tool-shell 里的实现是 shell 专用的。抽到这里,shell / MCP server / 外部插件共用**同一份**
// 平台实现与同一份开关语义 —— 避免"三套 profile 各自演化"。
//
// 档位来源(调用方负责):shell 从 `sdk.SandboxHint`(宿主随调用下传)取;MCP server 从
// 宿主在插件启动时注入的 GAH_EXT_SANDBOX_* 取。本包不猜档位:Mode 为空 = 不施加。
//
// 不可嵌套(实证):macOS 上进程已受 seatbelt 约束后再执行 `sandbox-exec` 得到
// `sandbox_apply: Operation not permitted`(spike 复现)。因此被施加约束的进程会把
// MarkerEnv=1 写进子进程环境,下游见 MarkerEnv 即**不再重复施加**(祖先的 profile 覆盖后代)。
//
// 平台能力:darwin = seatbelt;linux(amd64/arm64/…) = Landlock;其余平台**无**等价原语
// (Windows)→ 不施加但显式告警(不静默降级);详见 other.go。
//
// 关闭开关:由调用方经 Spec.Switch 指定(不同入口有各自的退路),本包只负责读。
package kernelsandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// MarkerEnv 已受内核沙箱约束的标记:被施加约束的进程把它置 1 传给子进程,
// 下游据此**不再重复施加**(seatbelt/Landlock 均不可嵌套,见包注释)。
const MarkerEnv = "GAH_KERNEL_SANDBOXED"

// Marked 当前进程是否已在某层内核沙箱内(祖先施加)。
func Marked() bool { return os.Getenv(MarkerEnv) == "1" }

// Spec 一次包装请求。零值 = 不施加。
type Spec struct {
	// Mode 有效档位(空 = 未知 → 不施加;FullAccess = 不需要 → 不施加)。
	Mode sdk.SandboxMode
	// Root workspace 根(workspace 档使用;为空 → 降级为 read-only:判不定的写更危险)。
	Root string
	// Jail 临时/缓存锚点(必需)。没有锚点时构建类命令会大面积失败,故宁可**不施加并告警**。
	Jail string
	// RW 额外可写路径(包管理器缓存、用户经环境变量显式点名的路径)。
	RW []string
	// ReadDeny 读取拒绝的目录(仅 shell 用;插件/第三方 server 默认 nil ——
	// 读凭据是它们的正当职责,默认拒会大面积打断)。见 sdk/credentialpath.go。
	ReadDeny []string
	// ReadDenySwitch ReadDeny 的显式关闭开关名(告警文案用)。
	ReadDenySwitch string
	// Switch 本调用方"关闭内核沙箱"的环境变量名(告警文案用;空则用通用名)。
	Switch string
	// Label 告警前缀("tool-shell" / "外部插件/MCP server")。空 = "kernelsandbox"。
	Label string
	// SandboxExec darwin 前端路径(空 = /usr/bin/sandbox-exec)。做成字段是为了可测:
	// "前端缺失 → 不施加"这条降级分支必须能被测试覆盖。
	SandboxExec string
	// RequireJailSwitch 若该开关为 "0",调用方认为白名单锚点失效 → 本包不施加并告警
	// (shell 的 GAH_SHELL_JAIL;空 = 无此概念)。文案由 why 说明。
	RequireJailSwitch string
}

var warnOnce sync.Once

var downgradeOnce sync.Once

// WarnUnavailable 一次性告警:内核沙箱本该生效却无法生效(安全边界降级必须可见)。
// 导出给调用方用自己的措辞说明"为什么这次不施加"(如 shell 的 jail 被关)。
func WarnUnavailable(spec Spec, why string) {
	warnOnce.Do(func() {
		fmt.Fprintf(os.Stderr,
			"gah %s: 内核级沙箱未生效(%s);当前仅工具边界协作式控制(写目标裁决 + 临时区)生效。%s%s\n",
			spec.label(), why, platformSupportNote(), switchHint(spec.Switch))
	})
}

// switchHint 生成"如需关闭该提示的触发条件,设置 X=0"的尾注(无开关则空)。
func switchHint(name string) string {
	if name == "" {
		return ""
	}
	return "如需关闭该提示的触发条件,设置 " + name + "=0。"
}

func (s Spec) label() string {
	if strings.TrimSpace(s.Label) == "" {
		return "kernelsandbox"
	}
	return s.Label
}

// Wrap 按 spec 返回要**前置到 argv** 的包装(空 = 本次不施加)。
//
// 不施加的四种情况只在需要时告警:显式关闭(不告警)、档位未知/全权档(不告警)、
// 已在内核沙箱内(不告警,是设计预期)、档位非法或缺少锚点(告警 —— 那是配置/状态异常)。
func Wrap(spec Spec) []string {
	if spec.Switch != "" && os.Getenv(spec.Switch) == "0" {
		return nil // 显式关闭:不施加,也不打扰
	}
	if Marked() {
		return nil // 祖先已施加,覆盖后代;重复施加会直接失败(不可嵌套)
	}
	if spec.RequireJailSwitch != "" && os.Getenv(spec.RequireJailSwitch) == "0" {
		WarnUnavailable(spec, "调用方已关闭临时区("+spec.RequireJailSwitch+"=0),白名单锚点失效")
		return nil
	}
	switch spec.Mode {
	case "":
		return nil // 档位未知:不猜
	case sdk.SandboxFullAccess:
		return nil // 全权档:协作层也不拦,内核层无需施加
	case sdk.SandboxReadOnly, sdk.SandboxWorkspace:
		// 继续
	default:
		WarnUnavailable(spec, "档位无法识别: "+string(spec.Mode))
		return nil
	}
	if strings.TrimSpace(spec.Jail) == "" {
		WarnUnavailable(spec, "缺少临时区锚点(jail 路径为空)")
		return nil
	}
	// workspace 根不可用 → 降级 read-only(**而不是不施加**):见 Normalized 的说明。
	spec, why := Normalized(spec)
	if why != "" {
		downgradeOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "gah %s: %s;本次按只读档施加内核约束(区外写仍被拒)\n", spec.label(), why)
		})
	}
	return platformWrap(spec)
}

// EnsureJailDir 确保临时区锚点存在并返回它。
//
// 为何单独一个函数:Linux 侧 bootstrap 把 jail 当"硬边界"加入规则,路径不存在 →
// `landlock_add_rule` 失败 → 整个包装失败(进程 exit 126)。而**独立进程**入口
// (MCP server / 外部插件)可能在 jail 首次使用前就启动(干净数据根),不能把"目录还没建"
// 当成配置错。shell 路径不靠它:shell 自己有 jailEnv(先建好再包),且"首条命令才建 jail"
// 是一条被测试钉住的语义(见 tool-shell kernel_test)。
//
// 失败只返回路径不报错:是否致命交给平台分支的硬规则判定(单一判据点)。
func EnsureJailDir() string {
	d := sdk.JailDir()
	if strings.TrimSpace(d) != "" {
		_ = os.MkdirAll(d, 0o700)
	}
	return d
}

// PrefixedArgv 拼接包装与真实命令。
//
// 必须用新底层数组:`append(pre, name)` 会在 pre 有余量时就地覆写调用方持有的切片 ——
// 调用方复用同一个 pre(如 PTY 与 shell 两条路径共用一次 kernelWrap 结果)时会串味。
func PrefixedArgv(pre []string, name string, rest ...string) []string {
	argv := make([]string, 0, len(pre)+1+len(rest))
	argv = append(argv, pre...)
	argv = append(argv, name)
	return append(argv, rest...)
}

// ResolvePath 解析软链;失败时逐级向上找最近的存在祖先再拼回剩余部分。
//
// 为什么必须解析(darwin 实测踩到):macOS 上 /tmp 是 /private/tmp 的软链,seatbelt 按**真实路径**
// 匹配,不解析则 `(subpath "/tmp/x")` 形同不设 —— 白名单看起来在、实际全放行。
// 路径**尚不存在**时(全新数据根的 jail、未创建的 workspace 子目录)直接回退原始路径会把软链前缀
// 交给内核匹配,白名单同样形同不设;整条链都不存在时回退原路径(保守方向:少放行,不多放行)。
func ResolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil && r != "" {
		return r
	}
	dir := filepath.Clean(p)
	var tail []string
	for {
		parent := filepath.Dir(dir)
		if parent == dir { // 已到根仍解析不出
			return p
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
		dir = parent
		if r, err := filepath.EvalSymlinks(dir); err == nil && r != "" {
			return filepath.Join(append([]string{r}, tail...)...)
		}
	}
}

// DefaultRWPaths 工具链最常见的"必须可写"落点(第三方 server / 插件进程要下载与缓存)。
//
// 依据 2026-09-27 spike(真实 MCP server 跑在只有 workspace+jail 的 profile 下):
// `npx` 起的 server 因写 ~/.npm 被拒直接启动失败(npm error EPERM .../_cacache/tmp)。
// 加上 ~/.npm 与 $TMPDIR 后同一 server 正常起、工具可调、区外写仍被内核拒。
//
// 只收**缓存/临时**性质的目录(不波及用户文件与配置):包管理器缓存 + 系统临时区。
// 不含 ~/Library/Application Support、~/.local/share 这类"应用数据"目录 ——
// 需要它们的 server 由用户经 RW 显式点名(RWPathsFromEnv),默认不放开。
func DefaultRWPaths() []string {
	var out []string
	// 家目录走 sdk.UserHomes()(**并集**):允许面与拒绝面同理,不能只认一家 —— Windows 上 MSYS 的
	// `~/.npm` 与原生 `%USERPROFILE%\.npm` 可能是两个目录,只白名单一个,另一个照样 EPERM
	// (2026-09-27 第二轮审计:F-A 同源的空档,只是方向相反)。
	for _, home := range sdk.UserHomes() {
		out = append(out,
			filepath.Join(home, ".npm"),
			filepath.Join(home, ".bun"),
			filepath.Join(home, ".cache"),
			filepath.Join(home, ".local", "state"), // uv/pipx 的 state(缓存/工具安装)
			filepath.Join(home, "Library", "Caches"),
			filepath.Join(home, "go", "pkg", "mod"), // GOFLAGS=-mod=mod 依赖下载落点
		)
	}
	if tmp := strings.TrimSpace(os.Getenv("TMPDIR")); tmp != "" {
		out = append(out, tmp)
	}
	out = append(out,
		"/tmp",
		"/private/tmp",
		"/private/var/folders", // macOS 真实 TMPDIR 根(/var/folders 是软链)
		"/var/tmp",
	)
	return out
}

// RWPathsFromEnv 解析用户显式点名的额外可写路径(冒号分隔,命名对齐既有 GAH_EXT_ENV_PASS)。
// 空段忽略;不做存在性检查(路径可稍后才创建)。
func RWPathsFromEnv(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, string(os.PathListSeparator)) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- 写入面:内核层与协作层的**单一事实源**(2026-10-03) ----
//
// 为何要有:此前「哪里能写」被推导了**两遍**,且两遍不等价 ——
//   - 内核层(darwinProfile / landlock 规则):工作区根(仅 workspace 档)+ jail + RW(包缓存/临时区);
//   - 协作层(policy-guard 的 ValidatePathAt,workspace 档):**只有工作区根**。
//
// 于是 `file_write $TMPDIR/x`、`~/.cache/y` 这类落点**内核放行、协作拒绝**。表现就是
// 用户看到的「做出操作后再判断、被回绝后再重新操作」:模型反复换路径重试,而两次裁决
// 说的根本不是同一件事。
//
// 收敛口径(不是放宽安全边界):
//   - 安全边界**仍然只由内核层守**(profile 不变,一处代码都没多放行);
//   - 协作层改为**按同一份清单**裁决,不再比内核更严地拒掉内核明确放行的落点;
//   - **内核层没在位时**(平台不支持 / 开关关 / jail 缺失 / 档位空)协作层保持原口径
//     (只在工作区根内)——那时它就是唯一的边界,放宽它等于 Windows 上直接失守。
//
// 这也是「把边界前置到内核层」的落法:先由内核定义边界,协作层与之对齐,而不是两层各写一套。
const (
	// writeScopeDeviceLiterals 设备节点字面量:内核 profile 需要,但**不属于目录白名单**
	// (协作层的路径判定按目录做,塞进清单只会得到一堆永不匹配的前缀)。
	writeScopeDeviceLiterals = "/dev/null,/dev/stdout,/dev/stderr,/dev/tty,/dev/ptmx"
)

// WritablePaths 该 spec 下的**写落点目录清单**(已按真实路径解析、去重、保序)。
//
// 供平台 profile 组装与协作层裁决共用(见文件上方说明)。不含设备字面量。
func WritablePaths(spec Spec) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p = strings.TrimSpace(p); p == "" {
			return
		}
		r := ResolvePath(p)
		if seen[r] {
			return
		}
		seen[r] = true
		out = append(out, r)
	}
	if spec.Mode == sdk.SandboxWorkspace {
		add(spec.Root)
	}
	// jail 两档都放行:否则 TMPDIR/GOCACHE 写不通,命令会大面积失败。
	add(spec.Jail)
	for _, p := range spec.RW {
		add(p)
	}
	return out
}

// DeviceLiterals 写放行的设备节点字面量(平台 profile 用;与 WritablePaths 分开是因为
// 它们的匹配语义不同:literal 不做子路径展开)。
func DeviceLiterals() []string {
	return strings.Split(writeScopeDeviceLiterals, ",")
}

// WouldApply 这份 spec 是否真的会施加内核约束,以及不施加的原因(**返回 true 时原因为空**)。
//
// 为何导出:协作层需要知道「内核层在场吗」—— 在场才敢按共享写入面裁决(见 WritablePaths
// 的口径),不在场就必须自己守住窄口径。同时它让「为什么没生效」从「静默」变成可断言的
// 事实(Wrap 的四类静默分支里,只有「档位未知/全权」是设计预期,其余都该被看见)。
func WouldApply(spec Spec) (bool, string) {
	if spec.Switch != "" && os.Getenv(spec.Switch) == "0" {
		return false, "已显式关闭(" + spec.Switch + "=0)"
	}
	if Marked() {
		// 祖先已施加:覆盖后代,重复施加会直接失败(不可嵌套)。这是设计预期,不算降级。
		return false, "已在祖先的内核沙箱内(覆盖后代)"
	}
	if spec.RequireJailSwitch != "" && os.Getenv(spec.RequireJailSwitch) == "0" {
		return false, "已关闭临时区(" + spec.RequireJailSwitch + "=0),白名单锚点失效"
	}
	switch spec.Mode {
	case "":
		return false, "档位未知(不猜)"
	case sdk.SandboxFullAccess:
		return false, "全权档(定义就是无边界)"
	case sdk.SandboxReadOnly, sdk.SandboxWorkspace:
	default:
		return false, "档位无法识别: " + string(spec.Mode)
	}
	if strings.TrimSpace(spec.Jail) == "" {
		return false, "缺少临时区锚点(jail 路径为空)"
	}
	// workspace 根不可用**不**判成「不施加」:内核层仍会以只读档生效(见 Normalized),
	// 协作层据此继续认为「内核在场」。这里只跑平台能力判定。
	if why := platformAvailableReason(spec); why != "" {
		return false, why
	}
	return true, ""
}

// Normalized 按内核能力把 spec 修正成「内核层真能施加的那一份」,并给出降级原因(空 = 未降级)。
//
// 降级只有一条:workspace 根**缺失或不存在** → 降为 read-only。
//
//   - 根为空:判不定的写更危险(既有语义)。
//   - 根不存在:**Landlock 要求规则路径存在**(`unix.Open(O_PATH)` 对缺失路径 ENOENT,
//     加不上规则就整体失败 → exit 126);macOS seatbelt 容忍不存在的 subpath,所以这是
//     **只有 Linux 会踩**的差异。2026-10-03 实测:host-jobs 的围栏补上后,Linux CI 上
//     一个 root 指向不存在目录的用例直接 126(命令跑不起来,任务 failed)。
//     降级成 read-only 仍然是**有围栏**(区外写一律被内核拒),只是不再放行工作区 ——
//     方向是收紧,不是放开。
//
// 为何要单列成函数:Wrap(施加)、WouldApply(协作层问「内核在场吗」)、以及协作层取共享
// 写入面这三处必须看到**同一份**修正后的 spec,否则会出现「内核按只读、协作层按工作区」
// 的错位 —— 比不同源更难查。
func Normalized(spec Spec) (Spec, string) {
	if spec.Mode != sdk.SandboxWorkspace {
		return spec, ""
	}
	root := strings.TrimSpace(spec.Root)
	switch {
	case root == "":
		spec.Mode = sdk.SandboxReadOnly
		return spec, "workspace 根未知"
	case !dirExists(root):
		spec.Mode = sdk.SandboxReadOnly
		return spec, "workspace 根不存在(" + root + ");内核规则要求该路径存在(Landlock 遇缺失路径会整体拒绝加规则)"
	}
	return spec, ""
}

// dirExists 目录是否存在(不可访问也算「不可用」——加不上规则就是加不上)。
func dirExists(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}
