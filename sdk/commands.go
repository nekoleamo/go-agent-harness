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
