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
	if spec.Mode == sdk.SandboxWorkspace && strings.TrimSpace(spec.Root) == "" {
		spec.Mode = sdk.SandboxReadOnly // 根未知 → 判不定的写更危险
	}
	return platformWrap(spec)
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
	if home, err := os.UserHomeDir(); err == nil && home != "" {
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
