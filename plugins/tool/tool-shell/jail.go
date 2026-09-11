// jail.go:shell 执行的环境 jail(R10 ① 收尾:把"无界的间接写"收敛到可盘点的单根)。
//
// 背景:shell 命令的**显式**写目标已由 policy-guard 裁决(plugins/policy/policy-guard/shellpaths.go),
// 但间接写(编译器/构建缓存、包管理器下载目录、工具自建的临时目录)无法从命令文本识别;
// 且本工具运行在外部插件进程(tool-basic)里,拿不到宿主沙箱档位(ctx 是桥桩,只有
// tools/jobs/fanout 回调)。所以这里做**档位无关**的恒定收敛:把"缓存根/临时根"重定向到
// 数据根下的 jail/ —— 越界写从"散落用户家目录与系统临时目录"变成"写在数据根内,可盘点、可整体清理"。
//
// 刻意不覆盖(边界,不是遗漏):
//   - HOME/GOPATH/CARGO_HOME/XDG_CONFIG_HOME:读配置与凭据(git/ssh/gpg)必须照常工作;
//   - 真正的家目录写(`echo x > ~/.zshrc`)由 policy-guard 的 shell 路径裁决拦截(重定向 + `~` 展开)。
//
// 关闭开关:GAH_SHELL_JAIL=0(调试/兼容);jail 建不起来时宁可显式失败,见 jailEnv。
package toolshell

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// jailEntry 一个被收敛的环境变量 → jail 内相对子目录(顺序固定,保证输出确定性与去重判定)。
type jailEntry struct {
	Key string
	Sub string
}

// jailEnvs 缓存根/临时根 → jail 子目录。TMP* 归 tmp(按 TTL 回收);
// 其余是缓存根(保留复用:每次清缓存等于每次冷启动,代价大于收益)。
var jailEnvs = []jailEntry{
	{"TMPDIR", "tmp"},
	{"TMP", "tmp"},
	{"TEMP", "tmp"},
	{"XDG_CACHE_HOME", "cache"},
	{"GOCACHE", "cache/go-build"},
	{"GOMODCACHE", "cache/go-mod"},
	{"npm_config_cache", "cache/npm"},
	{"PIP_CACHE_DIR", "cache/pip"},
}

// jailTTL jail/tmp 下条目的保留时长。
const jailTTL = 24 * time.Hour

// jailRoot 数据根下的 jail(便携纪律:一切经 sdk.Home() 派生,禁止家目录/cwd 硬拼)。
func jailRoot() string { return filepath.Join(sdk.Home(), "jail") }

// jailEnv 返回把 base 的缓存根/临时根收敛到 jail 内的环境变量副本。
// base 里缺的键会**补上**:Go/Node/pip 在变量缺失时会回落到用户家目录,不补则 jail 形同不设。
// 目录创建失败返回 error —— 安全边界不可用必须显式失败,不许静默放行。
func jailEnv(base []string) ([]string, error) {
	if os.Getenv("GAH_SHELL_JAIL") == "0" {
		return base, nil // 显式关闭:不动环境,也不建目录
	}
	root := jailRoot()
	override := make(map[string]string, len(jailEnvs))
	for _, e := range jailEnvs {
		dir := filepath.Join(root, e.Sub)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建 jail 目录 %s 失败: %w", dir, err)
		}
		override[e.Key] = dir
	}
	pruneJailTmp(filepath.Join(root, "tmp"))
	return mergeEnv(base, override), nil
}

// mergeEnv 用 override 覆盖 base 中的同名变量(同名重复只保留一次),再按固定顺序补上缺的键。
func mergeEnv(base []string, override map[string]string) []string {
	out := make([]string, 0, len(base)+len(override))
	seen := make(map[string]bool, len(override))
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if !ok { // 形态异常:原样保留,不猜
			out = append(out, kv)
			continue
		}
		val, hit := override[key]
		if !hit {
			out = append(out, kv)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key+"="+val)
	}
	for _, e := range jailEnvs {
		if !seen[e.Key] {
			out = append(out, e.Key+"="+override[e.Key])
		}
	}
	return out
}

// pruneJailTmp 回收 jail/tmp 下超过 TTL 的残留(mktemp 目录等)。best-effort:
// 回收失败不影响安全边界,不该让命令失败。缓存根不在此列(复用才有效)。
func pruneJailTmp(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-jailTTL)
	for _, e := range ents {
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}
