// 沙箱服务(ctx.sandbox):三档策略 + 路径校验(对齐设计 §8)。
// 模式:read-only(拒绝写/解释器)/ workspace-write(仅 workspace 根内写,默认)/ full-access(不拦截)。
package sdk

import "context"

// SandboxMode 沙箱档位。
type SandboxMode string

const (
	SandboxReadOnly   SandboxMode = "read-only"
	SandboxWorkspace  SandboxMode = "workspace-write"
	SandboxFullAccess SandboxMode = "full-access"
)

// Sandbox 服务:模式查询/切换 + 路径与命令校验。
type Sandbox interface {
	Mode() SandboxMode
	SetMode(m SandboxMode)
	// Root workspace 根目录(相对路径以它为根,防 ../ 穿越)。
	Root() string
	// ValidatePath 校验路径写权限:
	//   full-access: 放行
	//   workspace-write: 相对路径解析后必须在 Root 内;绝对路径在 Root 内或放行策略由实现决定
	//   read-only: 拒绝一切写(实现调用方用于写类行为)
	ValidatePath(p string) error
}

// ReadValidator 可选能力:读路径校验(实现 = policy-guard 沙箱)。
// 工具侧装配了实现此接口的沙箱时应优先调用(旧实现/测试替身不实现则退回各自兜底语义)。
type ReadValidator interface {
	// ValidateRead 校验读权限:full-access 放行;其余档位限 workspace 与数据根内;
	// 凭据类路径(provider.yaml/.ssh/*.pem 等)任何档位均拒。
	ValidateRead(p string) error
}

// EffectiveSandbox 可选能力:返回档位联动后的**有效**档位。
// approval 为权威档位时(open → full-access;strict → read-only),工具侧/状态展示
// 若只读 Mode() 会与实际拦截行为不一致;实现此接口即可对齐。
type EffectiveSandbox interface {
	EffectiveMode() SandboxMode
}

// SandboxHint 宿主在调用工具前注入的**有效沙箱上下文**(经 ctx 传递,不改 sdk.Tool 签名)。
//
// 为什么需要:外部进程插件里的工具(如 tool-basic 的 shell)只经回调通道与宿主通信,
// **拿不到 ctx.sandbox 服务** —— 档位联动(审批 open/strict 覆盖沙箱档)对它不可见。
// 宿主侧唯一执行入口(host-tools)在调用前注入本 hint,工具即可施加与档位一致的内核级约束
// (macOS seatbelt / Linux Landlock),而不必自行猜测档位。
//
// 缺省(hint 不存在)= 未注入(旧宿主/直连测试):工具**不得假定任何档位**,
// 应退回自身兜底语义(如仅环境 jail + 工具边界协作式控制)。
type SandboxHint struct {
	// Mode 有效档位(已含审批联动:open→full-access、strict→read-only)。
	Mode SandboxMode
	// Root workspace 根(绝对路径;相对写路径以它为根判定)。空 = 未知(按不可放行处理)。
	Root string
}

type sandboxHintKey struct{}

// WithSandboxHint 把有效沙箱上下文挂到 ctx 上(宿主执行入口调用)。
func WithSandboxHint(ctx context.Context, h SandboxHint) context.Context {
	return context.WithValue(ctx, sandboxHintKey{}, h)
}

// SandboxHintOf 读取有效沙箱上下文(未注入时 ok=false)。
func SandboxHintOf(ctx context.Context) (SandboxHint, bool) {
	if ctx == nil {
		return SandboxHint{}, false
	}
	h, ok := ctx.Value(sandboxHintKey{}).(SandboxHint)
	return h, ok
}
