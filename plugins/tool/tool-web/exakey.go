// Exa key 配置链(env 优先,配置文件兜底):EXA_API_KEY > $GAH_HOME/config/search.yaml > 空。
// 目的:不依赖启动 shell 的 env(常见终端会话固化问题)——写文件即被发现;
// GAH_HOME 由 main 统一贯通(宿主与 tool-basic 外部进程一致),文件路径两侧相同。
package toolweb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SearchConfig 搜索凭据配置文件 schema(~/.gah/config/search.yaml)。
type SearchConfig struct {
	Provider string `yaml:"provider,omitempty"` // 可选的 provider 兜底(缺省 exa)
	APIKey   string `yaml:"api_key,omitempty"`  // 无 env 时的 key 兜底
	Endpoint string `yaml:"endpoint,omitempty"` // 可选:自建/镜像端点
}

// searchConfigPath $GAH_HOME/config/search.yaml(GAH_HOME 未设时回退 ~/.gah)。
func searchConfigPath() string {
	home := os.Getenv("GAH_HOME")
	if home == "" {
		if uh, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(uh, ".gah")
		} else {
			home = os.TempDir()
		}
	}
	return filepath.Join(home, "config", "search.yaml")
}

// loadSearchConfig 读配置文件(缺文件/空文件 = 空配置,不报错;坏 yaml 显式报错)。
func loadSearchConfig() (SearchConfig, error) {
	raw, err := os.ReadFile(searchConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return SearchConfig{}, nil
		}
		return SearchConfig{}, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return SearchConfig{}, nil
	}
	var cfg SearchConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return SearchConfig{}, fmt.Errorf("tool-web: 解析 %s: %w", searchConfigPath(), err)
	}
	return cfg, nil
}

// resolveExaKey env 优先;无 env 时读配置文件 api_key(坏 yaml 显式失败,不静默)。
func resolveExaKey() (string, error) {
	if k := os.Getenv("EXA_API_KEY"); k != "" {
		return k, nil
	}
	cfg, err := loadSearchConfig()
	if err != nil {
		return "", err
	}
	return cfg.APIKey, nil
}

// resolveFileProvider 配置文件 provider 兜底(bundle data 未配时;空 = 未声明)。
func resolveFileProvider() string {
	cfg, err := loadSearchConfig()
	if err != nil {
		return ""
	}
	return cfg.Provider
}

// resolveFileEndpoint 配置文件端点兜底(可选镜像;空 = 官方端点)。
func resolveFileEndpoint() string {
	cfg, err := loadSearchConfig()
	if err != nil {
		return ""
	}
	return cfg.Endpoint
}
