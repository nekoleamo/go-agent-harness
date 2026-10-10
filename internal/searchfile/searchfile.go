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
//  1. env:GAH_SEARCH_API_KEY / EXA_API_KEY / ANYSEARCH_API_KEY / GAH_SEARCH_PROVIDER /
//     GAH_SEARCH_ENDPOINT / EXA_ENDPOINT / ANYSEARCH_ENDPOINT。
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
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// BackupDirName 备份目录名(与 core/config 的配置树备份同一处,便于整体备份一起带走)。
const BackupDirName = "config-backups"

var noteOnceDo sync.Once

// provider 名(注册表的键;新增 provider 在此登记)。
const (
	ProviderAnysearch = "anysearch"
	ProviderExa       = "exa"
)

// DefaultProvider 缺省 provider。
//
// 为什么是 anysearch 而不是 exa(2026-10-03 用户实测):exa 是**按量付费**的,额度耗尽直接回
// 402("搜索服务返回 402"),表现为「一搜索就坏」且无自愈;anysearch 允许**匿名调用**
// (不带 Authorization 头,按客户端 IP 计量日免费额度),国内直连可用,零配置即能搜。
// exa 保留为备选:需要更高召回质量时在 provider 一行切回去。
const DefaultProvider = ProviderAnysearch

// 配置项对应的环境变量名(注入契约的**唯一事实源**:宿主按这些键注入,插件按这些键读取)。
const (
	// EnvAPIKey 通用 key(不分 provider);provider 专属键命中时以专属键为准。
	EnvAPIKey = "GAH_SEARCH_API_KEY"
	// EnvExaAPIKey exa 专属 key(旧名,保留兼容:老 search.yaml/环境变量继续生效)。
	EnvExaAPIKey = "EXA_API_KEY"
	// EnvAnysearchAPIKey anysearch 专属 key(匿名即可用,配 key 只是提高并发与额度)。
	EnvAnysearchAPIKey = "ANYSEARCH_API_KEY"

	EnvProvider = "GAH_SEARCH_PROVIDER" // provider 名(缺省 anysearch)
	// EnvEndpoint 通用端点(自建/镜像/兼容服务);provider 专属端点命中时以专属端点为准。
	EnvEndpoint = "GAH_SEARCH_ENDPOINT"
	// EnvExaEndpoint / EnvAnysearchEndpoint provider 专属端点。
	EnvExaEndpoint       = "EXA_ENDPOINT"
	EnvAnysearchEndpoint = "ANYSEARCH_ENDPOINT"
)

// File 配置内容。字段名与既有 yaml 键保持一致(老文件继续可用)。
//
// **key 与 endpoint 都按 provider 分**(2026-10-03):两者的错误后果都是「静默地打错地方」——
// 把 exa 的 key 发给 anysearch 会被 401(网关不回落匿名),把 exa 的 endpoint 留在文件里而
// provider 已是 anysearch,请求就直接 POST 到 exa 了。切 provider 时只改一行
// `provider:` 就够,不必记得同时注释/取消注释另一处(实测踩到)。
// 解析优先级均为:provider 专属 > 通用 > 空(匿名/官方端点)。
type File struct {
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	// APIKey / Endpoint 通用值(对当前 provider 生效;老文件里的 Exa 配置走这里,继续可用)。
	APIKey   string `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	// Exa* / Anysearch* provider 专属值(优先于上面的通用值)。
	ExaAPIKey       string `yaml:"exa_api_key,omitempty" json:"exa_api_key,omitempty"`
	ExaEndpoint     string `yaml:"exa_endpoint,omitempty" json:"exa_endpoint,omitempty"`
	AnysearchAPIKey string `yaml:"anysearch_api_key,omitempty" json:"anysearch_api_key,omitempty"`
	// AnysearchEndpoint 少写一个 n 是手滑高发点,故注释里点明。
	AnysearchEndpoint string `yaml:"anysearch_endpoint,omitempty" json:"anysearch_endpoint,omitempty"`
}

// KeyFor 取该配置下指定 provider 的生效 key(专属 > 通用;空 = 匿名调用)。
func (f File) KeyFor(provider string) string {
	if v := f.providerField(provider, func(f File) string { return f.ExaAPIKey },
		func(f File) string { return f.AnysearchAPIKey }); v != "" {
		return v
	}
	return f.APIKey
}

// EndpointFor 取该配置下指定 provider 的生效端点(专属 > 通用;空 = 用该 provider 的官方端点)。
func (f File) EndpointFor(provider string) string {
	if v := f.providerField(provider, func(f File) string { return f.ExaEndpoint },
		func(f File) string { return f.AnysearchEndpoint }); v != "" {
		return v
	}
	return f.Endpoint
}

// providerField 按 provider 取专属字段(未知 provider → 空,调用方按缺省处理)。
// exa/anysearch 两家各写一个 switch 会与常量表漂,故把「取哪个字段」交给调用方的两个闭包。
func (f File) providerField(provider string, exa, anysearch func(File) string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderExa:
		return exa(f)
	case ProviderAnysearch:
		return anysearch(f)
	}
	return ""
}

// Path 配置文件绝对路径:数据根唯一经 sdk.Home()($GAH_HOME;嵌入/单测空则 TempDir),
// 不落 ~/.gah/XDG/cwd(便携纪律:配置随 gah-data 整体迁移)。
func Path() string {
	return filepath.Join(sdk.Home(), "config", "search.yaml")
}

// ConfigVersion 配置文件 schema 版本(写进文件头注释,与 bundle 样板的 seed-version 同一约定)。
//
// 为什么需要:本文件**不在 embed 样板里**(它是用户自己写的,含第三方 key),所以升级时
// 没有 EnsureSeed 那条「备份后覆盖」的路。schema 改了(k → 按 provider 分家)而文件还是旧形
// 时,后果是**静默**的 —— 通用 api_key 会被当作当前 provider 的 key 发出去,401。
// 有了版本号,升级后第一次读到就自动补齐成新形(见 migrate)。
const ConfigVersion = 2

// configVersionMarker 文件头里的版本标记前缀。
const configVersionMarker = "# search-config-version:"

// header 文件头(人读;yaml 注释行,解析时忽略)。首行是版本标记 —— Load 靠它决定要不要迁移,
// 改动字段形状时**必须 bump ConfigVersion**,否则老用户永远停在旧形。
func header() string {
	return fmt.Sprintf(`%s %d
# gah 联网搜索配置(便携:随 gah-data/config/ 迁移)
# provider  可省略 —— 缺省 anysearch(匿名即可用,国内直连);要换 exa 写 exa
# **key 与 endpoint 都按 provider 分**:切 provider 时只改上面这一行即可,两家配置可以并存。
# 串味的后果是**静默失败**,所以分家是必要的:
#   - key 发错家  → 网关 401/403 且**不回落匿名**(anysearch 的口径);
#   - endpoint 发错家 → 请求打到另一家,任何一方都不报错,只是搜不到想要的东西。
#   provider: anysearch
#   anysearch_api_key: xxx        # 留空 = 匿名调用(日免费额度)
#   anysearch_endpoint: https://… # 留空 = 官方端点
#   exa_api_key: xxx              # 留空 = 不带鉴权头(自建 Exa 兼容端点通常不需要)
#   exa_endpoint: https://api.exa.ai/search
#   api_key / endpoint            # **通用值**:对当前 provider 生效,会覆盖同名专属值
# 外部插件进程在 macOS 默认沙箱档下**读不到本文件**(内核凭据读拒),由宿主读值后经
# Capabilities.ConfigEnv 注入进程 env —— 两条通道等价,改本文件后重载插件即生效。
# 升级后本文件会被自动补齐到当前 schema(旧版备份在 config/config-backups/search.yaml.*)。
`, configVersionMarker, ConfigVersion)
}

// configVersion 解析文件头里的版本标记(没标记 = 1,即最早那种只有 provider/api_key/endpoint 的形状)。
func configVersion(raw []byte) int {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, configVersionMarker) {
			continue
		}
		f := strings.Fields(strings.TrimPrefix(line, configVersionMarker))
		if len(f) == 0 {
			return 1
		}
		if n, err := strconv.Atoi(f[0]); err == nil && n > 0 {
			return n
		}
		return 1
	}
	return 1
}

// Load 读配置文件。缺文件/空文件 = 零值(不报错);坏 yaml/其它读错 = 显式报错(不静默回退)。
//
// 顺带做**一次性的 schema 迁移**(见 ConfigVersion):读到旧版本就备份原文件、就地补齐成新形,
// 返回值一律是迁移后的结果 —— 即便落盘失败(只读数据根、外部插件进程被内核读拒)也一样,
// 免得「内存里已迁移、盘上还是旧形」造成两个真相。落盘失败只记一行,不阻断启动。
func Load() (File, error) {
	var f File
	path := Path()
	raw, err := os.ReadFile(path)
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
	if configVersion(raw) >= ConfigVersion {
		return f, nil
	}
	migrated, notes := migrate(f)
	if err := rewriteMigrated(path, migrated); err != nil {
		noteOnce("gah 搜索配置:已按新 schema 读取,但回写失败(%v);下次启动会再试", err)
	} else {
		noteOnce("gah 搜索配置:已升级到 schema v%d,旧版备份在 config/config-backups/search.yaml.*", ConfigVersion)
	}
	for _, n := range notes {
		noteOnce("gah 搜索配置: %s", n)
	}
	return migrated, nil
}

// migrate v1 → v2:通用 api_key/endpoint 拆进 provider 专属键,并把 provider 写实。
//
// **只搬能确定归属的**:v1 只有 exa 一个 provider,所以「没写 provider」或「写的 exa」时,
// 通用值必然是 exa 的 —— 搬进 exa_api_key/exa_endpoint,行为一字不变,且日后切 provider
// 不会再把它带过去。
//
// **归属不明的原样保留 + 提示**(provider 已是非 exa 却还留着通用值):那份值到底是给谁的
// 从文件里看不出来,自动搬就是猜 —— 猜错的表现是搜索静默失效(401 / 打到另一家服务)。
// 此时保留原值(行为与升级前完全一致),只把话说出来让用户自己挪。
func migrate(f File) (File, []string) {
	var notes []string
	out := f
	wasUnset := strings.TrimSpace(f.Provider) == ""
	if wasUnset {
		out.Provider = DefaultProvider // 从此显式:缺省不再是隐式行为
	}
	owner := ""
	if wasUnset || strings.EqualFold(strings.TrimSpace(f.Provider), ProviderExa) {
		owner = ProviderExa
	}
	if f.APIKey != "" {
		if owner != "" && f.ExaAPIKey == "" && f.AnysearchAPIKey == "" {
			out.ExaAPIKey, out.APIKey = f.APIKey, ""
		} else {
			notes = append(notes, fmt.Sprintf(
				"api_key 是**通用值**,现在会被当作当前 provider(%s)的 key 发出去;"+
					"若它其实是 exa 的,请移到 exa_api_key(留着会得到 401)", out.Provider))
		}
	}
	if f.Endpoint != "" {
		if owner != "" && f.ExaEndpoint == "" && f.AnysearchEndpoint == "" {
			out.ExaEndpoint, out.Endpoint = f.Endpoint, ""
		} else {
			notes = append(notes, fmt.Sprintf(
				"endpoint 是**通用值**,现在会用于当前 provider(%s);若它其实是 exa 的端点,"+
					"请移到 exa_endpoint(留着会静默打到另一家服务)", out.Provider))
		}
	}
	return out, notes
}

// rewriteMigrated 备份旧文件后按新 schema 重写(0600 原子替换;备份同样 0600 —— 含第三方 key)。
func rewriteMigrated(path string, f File) error {
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dir := filepath.Join(filepath.Dir(path), BackupDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	bak := filepath.Join(dir, fmt.Sprintf("search.yaml.%s", time.Now().Format("20060102-150405")))
	if err := writeFileAtomic(bak, old, 0o600); err != nil {
		return fmt.Errorf("备份失败 %s: %w", bak, err)
	}
	body, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append([]byte(header()), body...), 0o600)
}

// noteOnce 一次性提示(迁移这类事只该在升级后说一次,不该每次读文件都刷屏)。
func noteOnce(format string, args ...any) {
	noteOnceDo.Do(func() { fmt.Fprintf(os.Stderr, format+"\n", args...) })
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
	return writeFileAtomic(path, append([]byte(header()), raw...), 0o600)
}

// KnownEnvKeys 宿主允许注入 / 插件允许声明的键集合(白名单;排序保证日志与测试稳定)。
func KnownEnvKeys() []string {
	out := []string{
		EnvAPIKey, EnvExaAPIKey, EnvAnysearchAPIKey,
		EnvEndpoint, EnvExaEndpoint, EnvAnysearchEndpoint,
		EnvProvider,
	}
	sort.Strings(out)
	return out
}

// EnvValues 配置项 → 注入用的 env 键值(只含非空字段:没配的不注入,免得用空值覆盖插件默认)。
//
// 通用键与专属键**同时**给出:专属键供 provider 直读,通用键供"配置里没写 provider、
// 由 env 决定 provider"的场景兜底;两者同时命中时 provider 侧按专属键优先
// (见 KeyFor / EndpointFor)。
func EnvValues(f File) map[string]string {
	out := map[string]string{}
	if f.APIKey != "" {
		out[EnvAPIKey] = f.APIKey
	}
	if f.ExaAPIKey != "" {
		out[EnvExaAPIKey] = f.ExaAPIKey
	}
	if f.AnysearchAPIKey != "" {
		out[EnvAnysearchAPIKey] = f.AnysearchAPIKey
	}
	if f.Endpoint != "" {
		out[EnvEndpoint] = f.Endpoint
	}
	if f.ExaEndpoint != "" {
		out[EnvExaEndpoint] = f.ExaEndpoint
	}
	if f.AnysearchEndpoint != "" {
		out[EnvAnysearchEndpoint] = f.AnysearchEndpoint
	}
	if f.Provider != "" {
		out[EnvProvider] = f.Provider
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
		Provider:          os.Getenv(EnvProvider),
		APIKey:            os.Getenv(EnvAPIKey),
		ExaAPIKey:         os.Getenv(EnvExaAPIKey),
		AnysearchAPIKey:   os.Getenv(EnvAnysearchAPIKey),
		Endpoint:          os.Getenv(EnvEndpoint),
		ExaEndpoint:       os.Getenv(EnvExaEndpoint),
		AnysearchEndpoint: os.Getenv(EnvAnysearchEndpoint),
	}
	if env.Provider != "" && env.KeyFor(env.Provider) != "" && env.EndpointFor(env.Provider) != "" {
		return env, nil // 三项齐备:完全不必碰文件
	}
	file, err := Load()
	if err != nil {
		if env != (File{}) {
			return env, nil
		}
		return env, err
	}
	if env.Provider == "" {
		env.Provider = file.Provider
	}
	// 专属项各自独立补缺(env 的 exa key 不会被文件的 anysearch key 顶掉)。
	if env.ExaAPIKey == "" {
		env.ExaAPIKey = file.ExaAPIKey
	}
	if env.AnysearchAPIKey == "" {
		env.AnysearchAPIKey = file.AnysearchAPIKey
	}
	if env.ExaEndpoint == "" {
		env.ExaEndpoint = file.ExaEndpoint
	}
	if env.AnysearchEndpoint == "" {
		env.AnysearchEndpoint = file.AnysearchEndpoint
	}
	if env.APIKey == "" {
		env.APIKey = file.APIKey
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
	if err := sdk.ReplaceFile(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
