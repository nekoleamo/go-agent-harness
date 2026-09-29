// 搜索配置解析(tool-web 侧适配器,第八十五批):
// 真实读写与通道优先级已下沉 internal/searchfile(宿主 web/ 与 host-bridge 共用同一份实现,
// 避免两套 schema 漂移);本文件只保留 tool-web 的调用口径。
//
// 通道优先级(高 → 低):env(EXA_API_KEY / GAH_SEARCH_PROVIDER / GAH_SEARCH_ENDPOINT)
// > $GAH_HOME/config/search.yaml(**同一份数据**的两条送法)。
// 为什么要有 env:默认形态下 web_search 跑在外部插件 tool-basic 进程里,该进程在 macOS 默认
// 沙箱档下被内核凭据读拒挡在 $GAH_HOME/config 之外 ⇒ 文件写对了也读不到;宿主按
// Capabilities.ConfigEnv 声明把文件里的非空字段读出来注入进程 env,tool-web 只需 env 优先。
// 也因此:env 有命中时"文件读不到"不算配置错(见 searchfile.Resolve 的口径说明)。
package toolweb

import (
	"github.com/nekoleamo/go-agent-harness/internal/searchfile"
)

// SearchConfig 配置 schema(类型别名:单一事实源在 internal/searchfile)。
type SearchConfig = searchfile.File

// searchConfigPath $GAH_HOME/config/search.yaml(数据根唯一经 sdk.Home() 派生)。
func searchConfigPath() string { return searchfile.Path() }

// loadSearchConfig 读生效配置(env 优先,文件补缺)。
func loadSearchConfig() (SearchConfig, error) { return searchfile.Resolve() }

// resolveExaKey 生效 key(空 = 未配;请求不带 Authorization 头,自建端点通常不需要)。
func resolveExaKey() (string, error) {
	cfg, err := loadSearchConfig()
	if err != nil {
		return "", err
	}
	return cfg.APIKey, nil
}

// resolveFileProvider 生效 provider(bundle data 未配时兜底;空 = 未声明)。
func resolveFileProvider() string {
	cfg, err := loadSearchConfig()
	if err != nil {
		return "" // 读配置失败交给 provider 缺省;错误在构造 provider 时响亮(见 NewExaProvider)
	}
	return cfg.Provider
}

// resolveFileEndpoint 生效端点(空 = 官方端点)。
func resolveFileEndpoint() string {
	cfg, err := loadSearchConfig()
	if err != nil {
		return ""
	}
	return cfg.Endpoint
}
