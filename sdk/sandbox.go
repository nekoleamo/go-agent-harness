// 沙箱服务(ctx.sandbox):三档策略 + 路径校验(对齐设计 §8)。
// 模式:read-only(拒绝写/解释器)/ workspace-write(仅 workspace 根内写,默认)/ full-access(不拦截)。
package sdk

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
