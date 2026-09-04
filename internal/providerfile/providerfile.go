// Package providerfile 提供 LLM 提供商持久化层(~/.gah/config/provider.yaml)。
// TUI /provider set 写入(文件权限 0600,凭据不落其它可读位置、不进 seed/会话);
// env 优先于本文件(用户 shell 显式设置最高);适配器启动时经本包读取回退。
package providerfile

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Provider 提供商配置(base_url/api_key/model 三项)。
type Provider struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
}

// Path 配置文件绝对路径(GAH_HOME 覆盖;默认 ~/.gah/config/provider.yaml)。
func Path() string {
	home := os.Getenv("GAH_HOME")
	if home == "" {
		if uh, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(uh, ".gah")
		} else {
			home = os.TempDir()
		}
	}
	return filepath.Join(home, "config", "provider.yaml")
}

// Load 读取(文件不存在/空 = 空 Provider,不报错)。
func Load() (Provider, error) {
	var p Provider
	raw, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return p, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return p, nil
	}
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	return p, nil
}

// Save 写入(0600;仅显式 /provider set 调用)。
func Save(p Provider) error {
	raw, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// Clear 删除配置文件(回退 env/样板)。
func Clear() error {
	err := os.Remove(Path())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
