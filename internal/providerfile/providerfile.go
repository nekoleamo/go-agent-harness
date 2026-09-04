// Package providerfile 提供 LLM 提供商持久化层(~/.gah/config/provider.yaml)。
// TUI /provider set 写入(文件权限 0600,凭据不落其它可读位置、不进 seed/会话);
// env 优先于本文件(用户 shell 显式设置最高);适配器启动时经本包读取回退。
package providerfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Provider 提供商配置(base_url/api_key/model 三项;omitempty:逐项删除后不写出空字段)。
type Provider struct {
	BaseURL string `yaml:"base_url,omitempty"`
	APIKey  string `yaml:"api_key,omitempty"`
	Model   string `yaml:"model,omitempty"`
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

// UpdateModel 同步更新持久化 model(/model 联动:provider.yaml 存在时更新,
// 其余字段保留;无持久化 provider = no-op)。
func UpdateModel(model string) error {
	p, err := Load()
	if err != nil {
		return err
	}
	if p.BaseURL == "" && p.APIKey == "" {
		return nil // 无持久化 provider:无需联动
	}
	p.Model = model
	return Save(p)
}

// Clear 删除配置文件(回退 env/样板)。
func Clear() error {
	err := os.Remove(Path())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Unset 逐项删除某一字段(base_url|api_key|model),其余保留;全空时删除文件;
// 文件不存在 = no-op。
func Unset(field string) error {
	p, err := Load()
	if err != nil {
		return err
	}
	switch field {
	case "base_url":
		p.BaseURL = ""
	case "api_key":
		p.APIKey = ""
	case "model":
		p.Model = ""
	default:
		return fmt.Errorf("provider: 未知字段 %q(可选 base_url|api_key|model)", field)
	}
	if p.BaseURL == "" && p.APIKey == "" && p.Model == "" {
		return Clear() // 全空等价整文件删除
	}
	return Save(p)
}
