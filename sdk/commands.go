package sdk

// CommandSpec 一条用户命令(斜杠命令,如 /jobs list)。
// Name 不带斜杠;Run 收到除命令名外的参数(如 ["list"])。
// 插件在 Start 中向 ctx.commands 注册;TUI 提示列表与分发均来自注册表,
// 新增插件命令自动进入提示,卸载随 Disposer 撤销。
type CommandSpec struct {
	Name  string // 命令名(不含 /)
	Usage string // 完整用法(如 "/jobs list|output|kill")
	Desc  string // 一句话说明(提示列表展示)
	// Run 执行;返回输出文本(可多行,由 TUI 显示为 meta 行)与错误。
	Run func(args []string) (string, error)
	// Args 参数级联定义(交互式选择器):每级为枚举级(Options)或自由级
	// (FreeArgs,需手动输入)之一;picked 为前几级已选值(运行时动态求值,
	// 如插件/任务列表)。枚举选完 → 下一级;自由级 → 断点回输入框补参
	// (提示继续输入);无定义级 → 直接执行。零值 = 无参数级。
	Args []ArgLevel
}

// ArgLevel 一级参数定义:枚举(可选项)或自由(需手动输入,选择器断点)。
// 同一级只有一个生效:Options 非空用枚举;否则 FreeArgs 非空提示手动输入;
// 两者皆空 = 无定义(该路径直接执行)。
type ArgLevel struct {
	// Options 枚举选项(可空:该级非枚举);picked 为前几级已选值。
	Options func(picked []string) []Option
	// FreeArgs 自由参数名列表(可空);picked 同。非空 = 用户需手动输入这些参数。
	FreeArgs func(picked []string) []string
}

// Option 交互式选择器的一个选项(Value 为填入命令行的值,Desc 为说明)。
type Option struct {
	Value string
	Desc  string
}

// CommandRegistry 服务(ctx.commands):斜杠命令注册表(单一事实源)。
// 实现:plugins/host-commands(base bundle,先于依赖者启动)。
type CommandRegistry interface {
	// Register 注册命令,返回撤销 Disposer。同名冲突拒绝并返回错误(非静默)。
	Register(spec CommandSpec) (Disposer, error)
	// List 全部已注册命令(注册顺序,稳定)。
	List() []CommandSpec
	// Get 取回单个命令。
	Get(name string) (CommandSpec, bool)
}
