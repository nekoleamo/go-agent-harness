// Package searchfile 联网搜索配置的持久化层($GAH_HOME/config/search.yaml)。
//
// 为何单独成层(第八十五批):
//   - 配置此前只由 plugins/tool/tool-web 自己解析,于是"宿主代读并注入"这件事没有落点,
//     而默认形态下 web_search 跑在**外部插件 tool-basic 进程**里 —— 该进程在 macOS 默认沙箱档
//     下被内核凭据读拒挡在 $GAH_HOME/config 之外,写进文件也读不到(文档却长期写"推荐配置文件",
//     本批修正);
//   - 宿主(web/ + host-bridge)与插件(tool-web)此后共用本实现,避免两份 schema 漂移
//     (对齐 internal/mcpconfig 的单一事实源决策)。
//
// 两条通道(优先级 高 → 低):
//  1. env:EXA_API_KEY / GAH_SEARCH_PROVIDER / GAH_SEARCH_ENDPOINT。
//     外部插件由宿主按 Capabilities.ConfigEnv 声明**从本文件读值注入** —— 进程 env 是
//     "插件拿得到、模型经 shell 拿不到"的唯一通道(shell 子进程 env 经 sdk.SanitizedEnv
//     滤除 *_API_KEY;文件读则会被内核读拒挡死)。
//  2. 本文件(便携:随 gah-data/config/ 整体迁移;Linux/Windows 与进程内装配直接可用)。
package searchfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 配置项对应的环境变量名(注入契约的**唯一事实源**:宿主按这些键注入,插件按这些键读取)。
const (
	EnvAPIKey   = "EXA_API_KEY"         // 搜索服务 key
	EnvProvider = "GAH_SEARCH_PROVIDER" // provider 名(缺省 exa)
	EnvEndpoint = "GAH_SEARCH_ENDPOINT" // 自定义端点(自建 Exa 兼容服务)
)

// File 配置内容。字段名与既有 yaml 键保持一致(老文件继续可用)。
type File struct {
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	APIKey   string `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
}

// Path 配置文件绝对路径:数据根唯一经 sdk.Home()($GAH_HOME;嵌入/单测空则 TempDir),
// 不落 ~/.gah/XDG/cwd(便携纪律:配置随 gah-data 整体迁移)。
func Path() string {
	return filepath.Join(sdk.Home(), "config", "search.yaml")
}

// header 文件头(人读;yaml 注释行,解析时忽略)。
const header = `# gah 联网搜索配置(便携:随 gah-data/config/ 迁移)
# provider(可省略,缺省 exa)/ api_key(留空 = 不带鉴权头,自建端点通常不需要)/
# endpoint(可省略,缺省官方 https://api.exa.ai/search;自建 Exa 兼容端点可指环回地址)
# 外部插件进程在 macOS 默认沙箱档下**读不到本文件**(内核凭据读拒),由宿主读值后经
# Capabilities.ConfigEnv 注入进程 env —— 两条通道等价,改本文件后重载插件即生效。
`

// Load 读配置文件。缺文件/空文件 = 零值(不报错);坏 yaml/其它读错 = 显式报错(不静默回退)。
func Load() (File, error) {
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
		return f, fmt.Errorf("search.yaml 解析失败: %w", err)
	}
	return f, nil
}

// Save 写盘(0600 + 同目录临时文件原子替换;空字段不落盘 = 清空即删除该项)。
func Save(f File) error {
	raw, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, append([]byte(header), raw...), 0o600)
}

// KnownEnvKeys 宿主允许注入 / 插件允许声明的键集合(白名单;排序保证日志与测试稳定)。
func KnownEnvKeys() []string {
	out := []string{EnvAPIKey, EnvProvider, EnvEndpoint}
	sort.Strings(out)
	return out
}

// EnvValues 配置项 → 注入用的 env 键值(只含非空字段:没配的不注入,免得用空值覆盖插件默认)。
func EnvValues(f File) map[string]string {
	out := map[string]string{}
	if f.APIKey != "" {
		out[EnvAPIKey] = f.APIKey
	}
	if f.Provider != "" {
		out[EnvProvider] = f.Provider
	}
	if f.Endpoint != "" {
		out[EnvEndpoint] = f.Endpoint
	}
	return out
}

// Resolve 按通道优先级取生效配置(env 高,文件低)。
//
// 关键口径(第八十五批):**只要有任一 env 命中,文件就只作补缺,读不到即当空** ——
// 外部插件在 macOS 默认档下读本文件必 EPERM,而宿主此时已按声明把非空字段代读注入了,
// 再把"插件读不到文件"当配置错就会让搜索整个不可用(第八十五批修复前的实际表现)。
// 反之,一个 env 都没命中时读文件:坏 yaml/读错**响亮报错**(保持"坏配置不静默"的既有语义)。
func Resolve() (File, error) {
	env := File{
		Provider: os.Getenv(EnvProvider),
		APIKey:   os.Getenv(EnvAPIKey),
		Endpoint: os.Getenv(EnvEndpoint),
	}
	if env.Provider != "" && env.APIKey != "" && env.Endpoint != "" {
		return env, nil // 三项齐备:完全不必碰文件
	}
	file, err := Load()
	if err != nil {
		if env != (File{}) {
			return env, nil
		}
		return env, err
	}
	if env.APIKey == "" {
		env.APIKey = file.APIKey
	}
	if env.Provider == "" {
		env.Provider = file.Provider
	}
	if env.Endpoint == "" {
		env.Endpoint = file.Endpoint
	}
	return env, nil
}

// writeFileAtomic 同目录临时文件写入 + rename 原子替换(失败清理临时文件)。
func writeFileAtomic(path string, raw []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".search-*.tmp")
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
