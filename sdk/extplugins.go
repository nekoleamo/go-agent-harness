// extplugins.go:外部插件(host-bridge 装配的独立进程插件)的宿主侧控制面。
// 由 host-bridge Provide("ctx.extplugins");未装配 = 无该通道(消费方按可选注入处理)。
package sdk

// ExternalPlugins 外部插件运行时控制(宿主服务;sdk 只出契约,实现见 host-bridge)。
//
// 用途:NOND-M1 第 3 步 —— MCP server 配置($GAH_HOME/config/mcp.yaml)改完后,
// 让承接它的外部插件进程重读配置(工具集可能变:direct 全量 / search 只暴露代理工具),
// 免重启 gah。
type ExternalPlugins interface {
	// Reload 重启指定外部插件进程(名字 = 插件二进制的**文件基名**(去平台扩展名),
	// 如 "tool-mcp";发布布局 $GAH_HOME/plugins/tool-mcp/tool-mcp[.exe] 与
	// 扁平布局 $GAH_HOME/plugins/tool-mcp[.exe] 都能定位)。
	// 名字不存在/未安装 = 显式错误(不静默当成功,调用方须能区分"已重载"与"没生效")。
	Reload(name string) error

	// Disable 停用(不等于卸载!):进程停掉、工具与命令注销、文件留在原处、
	// 白名单条目与来源账条目**都留着**。
	//
	// 为什么要这条(2026-10-03,批二):此前只有 Reload ⇒ **没有任何运行时手段关掉一个
	// 行为不良的插件**。在"不验签名、后果自担"的模型下,它就是用户的止损手段。
	// 停用按**二进制**而不是按角色(一个 tool-kit 提供四个角色,用户的心智单位是一件)。
	// 未运行 = 记 no-op 返回(幂等):停用一个从没起来的插件不是错误。
	Disable(name string) error

	// Enable 启用:先走一遍**漂移判定**(tag 漂移 ⇒ 拒绝,与重装同款文案),
	// 再加载 + 注册。已运行 = 等价 Reload。
	//
	// 为什么启用要重验漂移:不重验的话,"停用"就成了绕过漂移守卫的后门 ——
	// 用户停用半年后回来,作者可能已经把 tag 换了。
	Enable(name string) error

	// List 已知的外部插件(名字/路径/角色/是否停用/来源摘要),供面板与 CLI 展示。
	// 未安装的二进制不在列 —— 清单描述的是「宿主认得的外部插件」,不是「目录里有什么」。
	List() []ExternalPluginInfo
}

// ExternalPluginInfo 一个外部插件的展示事实(批二)。
type ExternalPluginInfo struct {
	// Name 二进制基名(去平台扩展名)—— 启停与 Reload 都用它当键。
	Name string `json:"name"`
	// Path 二进制绝对路径(扁平或发布布局)。
	Path string `json:"path"`
	// Roles 该二进制提供的角色(单角色时为 [""]/单元素)。
	Roles []string `json:"roles,omitempty"`
	// Loaded 当前是否有进程在跑(已停用/未装上 ⇒ false)。
	Loaded bool `json:"loaded"`
	// Disabled 是否被用户停用。
	Disabled bool `json:"disabled"`
	// Source 来源摘要(仓库@ref · 短 sha),来自 internal/install 的来源账;
	// 拿不到(手工放置/账坏了)时为空字符串 —— 不猜。
	Source string `json:"source,omitempty"`
	// Reject 被拒绝加载的原因(白名单不符/探测失败);空 = 没被拒。
	Reject string `json:"reject,omitempty"`
}

// RejectedPlugin 一个**被拒绝加载**的外部插件(白名单不符 / 探测失败等)。
//
// 为什么要有这个面:插件被拒的表现是「工具整组消失」,而原因只在日志里 ——
// 用户看到的是「我装的插件不见了」,自证成本极高。把被拒清单经 API 暴露到设置面板的
// 插件段,让「被拦」与「没装」在界面上可区分(与工具名那条同样的理由)。
type RejectedPlugin struct {
	Name   string `json:"name"`
	Reason string `json:"reason"` // 人话原因(已含补救办法)
	// Kind 哪一类原因。**两类的补救办法完全不同**,混在一起用户只能猜:
	//
	//	trust = 哈希白名单不通过(文件被换过 / 没登记)⇒ 补救是 `gah -trust-plugin <名>`;
	//	load  = 压根没加载起来(启动失败 / 握手被拒 / 协议不兼容)⇒ 信任它也没用,
	//	        要做的是看 stderr 里的原因或重装。
	//
	// 空值 = trust(向后兼容:旧实现与旧消费方都不必改)。
	Kind string `json:"kind,omitempty"`
}

// RejectedPlugin 的 Kind 取值。
const (
	RejectedKindTrust = "trust" // 完整性闸拦下
	RejectedKindLoad  = "load"  // 加载失败(非信任问题)
)

// RejectedPlugins 可选能力:被拒绝加载的外部插件清单(实现方 = host-bridge)。
// 未实现 = 该宿主没有这个面(如极简 profile),调用方按空清单处理,不报错。
type RejectedPlugins interface {
	Rejected() []RejectedPlugin
}
