// 凭证存取(QQ 官方 Bot v2;AppID/AppSecret,同 ilink.Store 型)。
// 便携纪律:凭证入 $GAH_HOME/config/qqbot.yaml(0600,随目录迁移);access_token 不落盘
// (运行时经 TokenSource 换取+内存缓存,重启重取即可)。
package qqbot

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Store 凭证/状态持久化(yaml,0600;openid 由事件可得不落盘)。
type Store struct {
	Path string
}

// NewStore 构造凭证存储(路径由插件壳按 $GAH_HOME/config 派生)。
func NewStore(path string) *Store { return &Store{Path: path} }

// Load 读取凭证;文件缺失/空 = 未配置(不报错)。
func (s *Store) Load() (*Credentials, error) {
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Credentials{}, nil
		}
		return nil, fmt.Errorf("qqbot: 读凭证失败: %w", err)
	}
	var c Credentials
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("qqbot: 凭证解析失败: %w", err)
	}
	return &c, nil
}

// Save 落盘(0600;AppSecret 属密钥类,权限收紧)。
func (s *Store) Save(c *Credentials) error {
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Configured 是否已填 AppID/AppSecret(登录/启动判断)。
func (c *Credentials) Configured() bool { return c.AppID != "" && c.AppSecret != "" }
