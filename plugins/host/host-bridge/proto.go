// Package hostbridge 提供 host-bridge 插件:外部进程插件桥(崩溃隔离,对齐设计 §2.3/M5)。
// 协议:自建 stdio + net/rpc(gob)传输层,见 transport.go(旧版为 go-plugin net/rpc 模式)。
package hostbridge

// ToolServer 外部插件侧实现的 RPC 服务(net/rpc 方法签名)。
// 新协议(多工具):Definitions + ExecuteNamed;旧协议(单工具)保持兼容。
// M14 外部命令桥(可选):Commands + CommandOptions + RunCommand——旧插件未实现时
// 宿主按"无命令"处理(调用报 can't find method → 跳过),行为不变。
type ToolServer interface {
	Definitions(args struct{}, reply *string) error
	ExecuteNamed(args *ExecNamedArgs, reply *ExecReply) error
	Cancel(args *CancelArgs, reply *bool) error // 执行取消(按 CallID;旧宿主不调用)

	Commands(args struct{}, reply *string) error              // 命令定义枚举(JSON 数组)
	CommandOptions(args *CmdOptionsArgs, reply *string) error // 枚举级选项运行期求值
	RunCommand(args *RunCommandArgs, reply *ExecReply) error  // 命令执行(host→external)
}

// CmdOptionsArgs 枚举级选项请求(宿主 TUI 选择器求值时调用)。
type CmdOptionsArgs struct {
	Name   string
	Level  int
	Picked []string
}

// RunCommandArgs 命令执行请求(宿主斜杠命令分发 → 外部进程)。
type RunCommandArgs struct {
	Name string
	Args []string
}

// ExecArgs/ExecReply RPC 载荷。strings 传输(JSON),gob 可序列化。
// CallID/TimeoutMs 为执行可中断契约(⑥):宿主为每次调用生成 CallID,插件按 CallID
// 登记可取消 ctx —— 宿主回合取消 / 超时后经 Plugin.Cancel RPC 真正中断外部执行
// (此前只传参数不传 ctx,长耗时外部工具只能靠宿主侧超时兜底,回合取消传不到插件)。
// 旧插件忽略未知字段(gob 按名匹配),行为不变。
type ExecArgs struct {
	JSONArgs  string
	CallID    string // 本次调用标识(空 = 不登记取消,旧宿主兼容)
	TimeoutMs int64  // 插件侧执行超时(毫秒;0 = 不限,由宿主侧超时 + Cancel 兜底)
	// SandboxMode/WorkspaceRoot 宿主下传的**有效**沙箱档位与 workspace 根
	// (见 sdk.SandboxHint;空 = 未注入/旧宿主 → 插件不得假定档位;
	// 工具侧据此施加与档位一致的内核级约束,见 plugins/tool/tool-shell)。
	SandboxMode   string
	WorkspaceRoot string
}

// ExecNamedArgs 新协议载荷:工具名 + 参数(含可中断契约,同 ExecArgs)。
type ExecNamedArgs struct {
	Name      string
	JSONArgs  string
	CallID    string
	TimeoutMs int64
	// SandboxMode/WorkspaceRoot 同 ExecArgs(有效档位下传)。
	SandboxMode   string
	WorkspaceRoot string
}

// CancelArgs 执行取消请求(宿主 → 外部插件;按 CallID 中断运行中的工具调用)。
type CancelArgs struct {
	CallID string
}

type ExecReply struct {
	Content string // 结果 JSON/文本
	Error   string // 结构化错误(非空 = 失败,不中断 turn)
}

// CommandDTO 命令定义载荷(M14 外部命令桥;Args 级联声明,对齐 sdk.ArgLevel 语义)。
// 每级:Enum=true 枚举级(选项运行期经 CommandOptions 求值);FreeArgs 非空=自由级
// (参数名序列,名尾 '?' 可选参数);皆空=无定义级(该路径直接执行)。
// TimeoutMs:命令执行/枚举选项 RPC 超时(毫秒),0 = 宿主全局默认(3s),对齐工具级语义。
type CommandDTO struct {
	Name      string          `json:"Name"`
	Usage     string          `json:"Usage"`
	Desc      string          `json:"Desc"`
	TimeoutMs int64           `json:"TimeoutMs,omitempty"`
	Args      []CommandArgDTO `json:"Args"`
}

// CommandArgDTO 一级参数的声明(见 CommandDTO)。
type CommandArgDTO struct {
	FreeArgs []string `json:"FreeArgs,omitempty"`
	Enum     bool     `json:"Enum,omitempty"`
}
