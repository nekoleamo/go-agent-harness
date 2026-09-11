// Package providerfile 提供 LLM 提供商持久化层(~/.gah/config/provider.yaml)。
// v2 多 provider 并存:文件为 {active, providers[]}(每项 name/base_url/api_key/model);
// 旧单对象格式(顶层 base_url/api_key/model)读取自动迁移视图(name=域短名/缺省 default),
// 下次写操作落 v2。文件权限 0600,凭据不落其它可读位置、不进 seed/会话;
// env 优先于文件(用户 shell 显式设置最高);适配器启动时经本包读取活动 provider 回退。
package providerfile

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Provider 一个提供商(base_url/api_key/model 三项;omitempty:逐项删除后不写出空字段)。
// Name 为标识/切换名(域短名;旧格式迁移时生成)。
type Provider struct {
	Name    string `yaml:"name,omitempty"`
	BaseURL string `yaml:"base_url,omitempty"`
	APIKey  string `yaml:"api_key,omitempty"`
	Model   string `yaml:"model,omitempty"`
}

// File 配置文件内容(v2):active = 启动默认活跃 provider 名;空 = 无/首个自动。
type File struct {
	Active    string     `yaml:"active,omitempty"`
	Providers []Provider `yaml:"providers,omitempty"`
}

// legacy 旧单对象格式(顶层字段;读取时迁移视图)。
type legacy struct {
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

// LoadFile 读取 v2 文件(旧单对象自动迁移为单 provider 视图,不改盘)。
// 文件不存在/空 = 空 File,不报错;坏 yaml 显式报错(不静默回退)。
func LoadFile() (File, error) {
	var f File
	raw, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return f, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return f, nil
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return f, err
	}
	// 旧格式迁移:无 providers 且无 active 时,尝试顶层旧字段。
	if len(f.Providers) == 0 && f.Active == "" {
		var lg legacy
		// 顶层字段(旧)与 v2 键不冲突(unmarshal 已忽略多余键;re-unmarshal 到 legacy 仅拾顶层)。
		if err := yaml.Unmarshal(raw, &lg); err == nil && (lg.BaseURL != "" || lg.APIKey != "" || lg.Model != "") {
			name := ShortNameOf(lg.BaseURL)
			f.Active = name
			f.Providers = []Provider{{Name: name, BaseURL: lg.BaseURL, APIKey: lg.APIKey, Model: lg.Model}}
		}
	}
	return f, nil
}

// SaveFile 写盘(0600;整文件原子覆盖——文件小,多 provider 单配置体)。
func SaveFile(f File) error {
	raw, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, raw, 0o600)
}

// writeFileAtomic 同目录临时文件写入 + rename 原子替换(失败清理临时文件)。
// 目的:读方(适配器启动解析、UI 查看)永不看到半截 YAML。
func writeFileAtomic(path string, raw []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".provider-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Load 活动 provider(旧 reader:adapter resolveConfig / UpdateModel / Unset 等)。
// 无 provider/缺文件 = 空 Provider,不报错。
func Load() (Provider, error) {
	f, err := LoadFile()
	if err != nil {
		return Provider{}, err
	}
	p, ok := find(f, f.Active)
	if !ok {
		return Provider{}, nil
	}
	return p, nil
}

// Active 当前活跃 provider 名(空 = 无)。
func Active() string {
	f, err := LoadFile()
	if err != nil {
		return ""
	}
	return f.Active
}

// find 按名查 provider。
func find(f File, name string) (Provider, bool) {
	for _, p := range f.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}

// Add 新增/更新 provider(upsert,非空字段覆盖、空字段保留旧):name 空 → 自动域短名。
// 首个 provider 自动设为 active;同名更新时同样激活(立即生效语义由运行时层配合)。
func Add(p Provider) error {
	f, err := LoadFile()
	if err != nil {
		return err
	}
	if p.Name == "" {
		p.Name = ShortNameOf(p.BaseURL)
	}
	idx := -1
	for i, x := range f.Providers {
		if x.Name == p.Name {
			idx = i
			break
		}
	}
	if idx >= 0 {
		old := f.Providers[idx]
		f.Providers[idx] = mergeFields(p, old)
		f.Active = p.Name // 同名更新 = 激活
	} else {
		f.Providers = append(f.Providers, p)
		if f.Active == "" || len(f.Providers) == 1 {
			f.Active = p.Name // 首个自动活跃
		}
	}
	return SaveFile(f)
}

// SetFields 按名局部更新(base/apiKey/model 非空覆盖;空保留旧)。name 不存在 = no-op 不写盘。
func SetFields(name, baseURL, apiKey, model string) error {
	if name == "" {
		name = Active()
	}
	if name == "" {
		return nil
	}
	f, err := LoadFile()
	if err != nil {
		return err
	}
	idx := -1
	for i, x := range f.Providers {
		if x.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	f.Providers[idx] = mergeFields(Provider{BaseURL: baseURL, APIKey: apiKey, Model: model}, f.Providers[idx])
	return SaveFile(f)
}

// mergeFields partial 合并:src 非空字段覆盖 dst,空字段保留 dst。
func mergeFields(src, dst Provider) Provider {
	if src.BaseURL != "" {
		dst.BaseURL = src.BaseURL
	}
	if src.APIKey != "" {
		dst.APIKey = src.APIKey
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.Name != "" {
		dst.Name = src.Name
	}
	return dst
}

// SetActive 切换活跃(校验存在;空名 = 清除活跃)。持久化 active。
func SetActive(name string) error {
	f, err := LoadFile()
	if err != nil {
		return err
	}
	if name == "" {
		f.Active = ""
		return SaveFile(f)
	}
	if _, ok := find(f, name); !ok {
		return fmt.Errorf("provider: 不存在 %q(/provider show 查看)",
			name)
	}
	f.Active = name
	return SaveFile(f)
}

// Remove 删除 provider;删除的是 active 时 active 重置(剩余首个,无则清空)。
func Remove(name string) error {
	f, err := LoadFile()
	if err != nil {
		return err
	}
	out := f.Providers[:0]
	for _, p := range f.Providers {
		if p.Name != name {
			out = append(out, p)
		}
	}
	if len(out) == len(f.Providers) {
		return nil // 不存在:no-op
	}
	f.Providers = out
	if f.Active == name {
		if len(out) > 0 {
			f.Active = out[0].Name
		} else {
			f.Active = ""
		}
	}
	if len(f.Providers) == 0 {
		return Clear()
	}
	return SaveFile(f)
}

// UpdateModel 同步更新**活跃** provider 的 model(/model 联动;无活跃/无 provider = no-op 不写盘)。
func UpdateModel(model string) error {
	if model == "" {
		return nil
	}
	f, err := LoadFile()
	if err != nil {
		return err
	}
	idx := -1
	for i, x := range f.Providers {
		if x.Name == f.Active {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil // 无活跃 provider:无需联动
	}
	f.Providers[idx].Model = model
	return SaveFile(f)
}

// Unset 逐项删除**活跃** provider 的字段(base_url|api_key|model);其余保留。
// 活跃被删空 → 从列表移除;列表空 → 删文件。文件不存在 = no-op。
func Unset(field string) error {
	switch field {
	case "base_url", "api_key", "model":
	default:
		return fmt.Errorf("provider: 未知字段 %q(可选 base_url|api_key|model)", field)
	}
	f, err := LoadFile()
	if err != nil {
		return err
	}
	idx := -1
	for i, x := range f.Providers {
		if x.Name == f.Active {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	p := f.Providers[idx]
	switch field {
	case "base_url":
		p.BaseURL = ""
	case "api_key":
		p.APIKey = ""
	case "model":
		p.Model = ""
	}
	if p.BaseURL == "" && p.APIKey == "" && p.Model == "" {
		return Remove(p.Name) // 全空:移除该 provider(含 active 重置)
	}
	f.Providers[idx] = p
	return SaveFile(f)
}

// Clear 删除配置文件(全部 provider 清除;回退 env/样板)。
func Clear() error {
	err := os.Remove(Path())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ShortNameOf baseURL → 域短名(name 生成与来源标注;api.siliconflow.cn → siliconflow)。
// 解析失败/无主机 → 去协议前缀的原文;空 → "default"(稳定名)。
func ShortNameOf(baseURL string) string {
	u := strings.TrimSpace(baseURL)
	if u == "" {
		return "default"
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Hostname() == "" {
		h := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		if h == "" {
			return "default"
		}
		return h
	}
	h := strings.TrimPrefix(parsed.Hostname(), "api.")
	if i := strings.Index(h, "."); i > 0 {
		h = h[:i]
	}
	if h == "" {
		return "default"
	}
	return h
}
