// 插件管理相关接口(插件唯一依赖;宿主通过 system.registry 服务注入 RegistryOps)。
package sdk

// RegistryOps 插件可用的注册表操作子集(运行期启停)。
// 完整拓扑/装配逻辑在 core/plugin(插件不可见)。
type RegistryOps interface {
	StartOne(c Ctx, id string) error
	Dispose(id string)
	Snapshot() []string
}

// PluginInfo 运行/配置三源清单条目。
type PluginInfo struct {
	ID     string
	Type   string
	Bundle string
	State  string // loaded(运行中)| configured(已注册未运行)| loaded(外部)
}

// PluginManager 服务(ctx.pluginManager):运行期插拔。
type PluginManager interface {
	List() []PluginInfo
	Load(id string) error
	Unload(id string) error
}
