// 插件管理相关接口(插件唯一依赖;宿主通过 system.registry 服务注入 RegistryOps)。
package sdk

// RegistryOps 插件可用的注册表操作子集(运行期启停)。
// 完整拓扑/装配逻辑在 core/plugin(插件不可见)。
type RegistryOps interface {
	StartOne(c Ctx, id string) error
	Dispose(id string)
	Snapshot() []string
	// BlockedByLoaded 返回仍加载且依赖 id 所提供服务键的插件列表(非空 = 运行期卸载会破坏它们)。
	BlockedByLoaded(id string) []string
}

// PluginInfo 运行/配置三源清单条目。
type PluginInfo struct {
	ID     string
	Type   string
	Bundle string
	State  string // loaded(运行中)| configured(已注册未运行)| loaded(外部)
	// Manage 声明管理域(单一事实源=catalogue 声明,web/tui 展示层透传,勿另行硬编码):
	// external(已外部化,勿启停)| scenario(场景专用,勿启)| 空(常规,按运行态派生 host/web)。
	Manage string
}

// PluginManager 服务(ctx.pluginManager):运行期插拔。
type PluginManager interface {
	List() []PluginInfo
	Load(id string) error
	Unload(id string) error
}
