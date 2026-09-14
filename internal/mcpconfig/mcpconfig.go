// Package mcpconfig 提供 MCP server 连接配置持久化层($GAH_HOME/config/mcp.yaml)。
//
// NOND-M1 第 2 步(按 server 门控):每个 server 一项 {name, command, args, enabled, mode},
// mode = direct(默认,工具全量注册,现状语义)或 search(工具不进上下文,模型只见
// mcp_search/mcp_call 两个代理工具)。**配置放 config/ 随数据根迁移**(便携纪律:
// 不得以 shell profile / 只读 env 作为唯一承载),可被 GUI 增删改。
//
// 兼容与优先级:env(`GAH_MCP_COMMANDS` 每行 name=command / `GAH_MCP_COMMAND` 单 server)
// 照旧生效 —— **文件条目优先**(同名以文件为准,可经 GUI 改模式),env 独有条目照旧装配
// 并在装配视图里标 source=env(只读展示)。无文件 = 行为与改动前一致。
package mcpconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 模式(direct = 现状全量注册;search = 只暴露 mcp_search/mcp_call)。
const (
	ModeDirect = "direct"
	ModeSearch = "search"
)

// 条目来源(装配视图用,不落盘):file = mcp.yaml 可经 GUI 改;env = 启动环境变量只读。
const (
	SourceFile = "file"
	SourceEnv  = "env"
)

// Server 一个 MCP server 连接项。
// Enabled 用指针:缺省(nil)= 启用 —— 手写配置时不必写 enabled: true。
type Server struct {
	Name    string   `yaml:"name" json:"name"`
	Command string   `yaml:"command" json:"command"`
	Args    []string `yaml:"args,omitempty" json:"args,omitempty"`
	Enabled *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Mode    string   `yaml:"mode,omitempty" json:"mode,omitempty"`
	// Source 装配视图来源(file|env);yaml:"-" 保证永不写回文件。
	Source string `yaml:"-" json:"source,omitempty"`
}

// IsEnabled 是否启用(缺省启用)。
func (s Server) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// ModeOrDefault 生效模式(缺省 direct = 现状)。
func (s Server) ModeOrDefault() string {
	if s.Mode == "" {
		return ModeDirect
	}
	return s.Mode
}

// File 配置文件内容。
type File struct {
	Servers []Server `yaml:"servers,omitempty"`
}

// Path 配置文件绝对路径:数据根唯一经 sdk.Home()($GAH_HOME;嵌入/单测空则 TempDir),
// 不落 ~/.gah/XDG/cwd(便携纪律:配置随 gah-data 整体迁移)。
func Path() string {
	return filepath.Join(sdk.Home(), "config", "mcp.yaml")
}

// LoadFile 读取配置文件。文件不存在/空 = 空 File(不报错);坏 yaml 显式报错(不静默回退)。
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
		return f, fmt.Errorf("mcp.yaml 解析失败: %w", err)
	}
	// 手写配置与 GUI 写入共用同一条规范化语义(名字净化 + 整行命令拆分):
	// 否则「GUI 写同样的命令能跑、手抄到文件里却报 no such file or directory」。
	// 校验类问题(空 name/非法 mode)不在这里吞掉,留给装配期显式报错。
	for i := range f.Servers {
		f.Servers[i] = normalizeLenient(f.Servers[i])
	}
	return f, nil
}

// SaveFile 写盘(0600;整文件原子覆盖 + 头部说明注释)。
// 只写文件条目(Source==env 的条目由 env 提供,不落盘,避免把只读来源固化成文件)。
func SaveFile(f File) error {
	var keep []Server
	for _, s := range f.Servers {
		if s.Source == SourceEnv {
			continue
		}
		s.Source = ""
		keep = append(keep, s)
	}
	f.Servers = keep
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

// header 文件头(人读;yaml 注释行,解析时忽略)。
const header = `# gah MCP server 配置(便携:随 gah-data/config/ 迁移)
# 每项:name(工具名前缀 mcp_<name>_<工具>,仅 [A-Za-z0-9-_],其余字符自动转 _)
#       command(启动命令;含空格时按引号规则拆分为 command+args,路径含空格请用引号)
#       args(可选,列表;与 command 二选一)
#       enabled(可省略,缺省 true)
#       mode(direct 默认 = 工具全量进上下文;search = 只暴露 mcp_search/mcp_call,按需搜索)
# env(GAH_MCP_COMMANDS / GAH_MCP_COMMAND)照旧生效,同名以本文件为准。
`

// writeFileAtomic 同目录临时文件写入 + rename 原子替换(失败清理临时文件)。
func writeFileAtomic(path string, raw []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcp-*.tmp")
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

// CleanName server 名 sanitize(工具名可安全拼接):仅保留 [A-Za-z0-9-_],其余转 _。
// 与外部进程历史行为一致(此前在 extplugins/tool-mcp 内实现,现下沉为单一事实源)。
func CleanName(n string) string {
	var sb strings.Builder
	for _, r := range strings.TrimSpace(n) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	return sb.String()
}

// Normalize 规范化一项(编辑器/GUI 写入路径共用):
//   - name:trim + CleanName;仍为空 → 报错(name 决定工具名前缀,不能猜);
//   - mode:空 = direct,非法值显式报错(不静默按 direct 处理);
//   - command:trim;含空白且 args 为空时按 sdk.SplitArgs 拆分(引号内空格保留 ——
//     路径含空格请用引号,如 "/Applications/My App/bin/x" --serve);
//   - enabled:原样(缺省 = 启用)。
func Normalize(s Server) (Server, error) {
	s = normalizeLenient(s)
	if s.Name == "" {
		return Server{}, fmt.Errorf("mcp: 缺少 server 名(name 决定工具名前缀 mcp_<name>_<tool>)")
	}
	if s.Mode != "" && s.Mode != ModeDirect && s.Mode != ModeSearch {
		return Server{}, fmt.Errorf("mcp: server %q 的 mode %q 非法(可选 %s|%s)", s.Name, s.Mode, ModeDirect, ModeSearch)
	}
	if s.Command == "" {
		return Server{}, fmt.Errorf("mcp: server %q 缺少 command(启动命令)", s.Name)
	}
	return s, nil
}

// splitCommand 按空白拆分命令行,双/单引号内的空白保留(路径含空格请写
// `"/Applications/My App/bin/x" --serve`),不做反斜杠转义。
//
// 为什么不用 sdk.SplitArgs:那个按 POSIX shell 语义把 \ 当转义符消费,而本字段的
// 首项是**直接 exec 的 argv[0]**,不是 shell 表达式。Windows 路径(C:\Users\…)
// 经它一拆就变成 C:Users…,server 起不来(表现为整组 mcp_<name>_* 工具缺席)。
// POSIX 路径不含反斜杠,所以两种实现在那边等价。
func splitCommand(s string) []string {
	var out []string
	var cur []rune
	started := false
	var quote rune // 0 = 未在引号内
	for _, r := range s {
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			cur = append(cur, r)
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if started {
				out = append(out, string(cur))
				cur = cur[:0]
				started = false
			}
		default:
			cur = append(cur, r)
			started = true
		}
	}
	if started {
		out = append(out, string(cur))
	}
	return out
}

// normalizeLenient 无需校验的规范化(读盘与写盘两条路径共用,保证语义单一):
// name → CleanName;mode → 小写 trim(写错大小写不算错,非法值仍由 Normalize 拒);
// command → trim,含空白且 args 为空时按 splitCommand 拆出 command+args。
func normalizeLenient(s Server) Server {
	s.Name = CleanName(s.Name)
	s.Mode = strings.ToLower(strings.TrimSpace(s.Mode))
	s.Command = strings.TrimSpace(s.Command)
	if len(s.Args) == 0 && strings.ContainsAny(s.Command, " \t") {
		if parts := splitCommand(s.Command); len(parts) > 0 {
			s.Command, s.Args = parts[0], parts[1:]
		}
	}
	return s
}

// ParseEnv 解析启动环境变量(向后兼容入口,行为与外部进程历史实现一致):
//   - single:`GAH_MCP_COMMAND`(单 server,空格分隔参数;name 为空 → 工具名不带 server 前缀)
//   - multi :`GAH_MCP_COMMANDS` 每行 `name=command args`(# 开头为注释,空行忽略)
//
// 返回值 notes 为需打印的问题行(无效行/重名),不阻断其余条目。
func ParseEnv(single, multi string) ([]Server, []string) {
	var out []Server
	var notes []string
	if strings.TrimSpace(single) != "" {
		parts := splitCommand(single)
		out = append(out, Server{Command: parts[0], Args: parts[1:], Source: SourceEnv, Mode: ModeDirect})
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(multi, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, cmd, found := strings.Cut(line, "=")
		name, cmd = strings.TrimSpace(name), strings.TrimSpace(cmd)
		if !found || name == "" || cmd == "" {
			notes = append(notes, fmt.Sprintf("忽略无效行(需 name=command):%q", line))
			continue
		}
		name = CleanName(name)
		if seen[name] {
			notes = append(notes, fmt.Sprintf("环境变量中重复的 server 名 %q(保留先出现项)", name))
			continue
		}
		seen[name] = true
		parts := splitCommand(cmd)
		out = append(out, Server{Name: name, Command: parts[0], Args: parts[1:], Source: SourceEnv, Mode: ModeDirect})
	}
	return out, notes
}

// Effective 合并文件条目与环境条目:文件优先(同名覆盖 env),env 独有条目追加在后。
// 返回顺序稳定(文件顺序 → env 顺序),供装配与 UI 复用同一份视图。
func Effective(file []Server, env []Server) []Server {
	byName := map[string]bool{}
	out := make([]Server, 0, len(file)+len(env))
	for _, s := range file {
		s.Source = SourceFile
		if s.Name != "" {
			if byName[s.Name] {
				continue // 文件内重名:保留先出现项(装配层另有冲突提示)
			}
			byName[s.Name] = true
		}
		out = append(out, s)
	}
	for _, s := range env {
		if s.Name != "" && byName[s.Name] {
			continue // 文件同名条目优先
		}
		s.Mode = s.ModeOrDefault()
		s.Source = SourceEnv // 环境来源(只读展示;调用方可不预置)
		out = append(out, s)
	}
	return out
}

// Load 装配视图:文件 ∪ env(文件优先)。返回有效条目、需打印的提示、读取错误
// (文件坏 yaml = 显式错误;env 无效行只记提示)。
func Load() ([]Server, []string, error) {
	f, err := LoadFile()
	if err != nil {
		return nil, nil, err
	}
	env, notes := ParseEnv(os.Getenv("GAH_MCP_COMMAND"), os.Getenv("GAH_MCP_COMMANDS"))
	return Effective(f.Servers, env), notes, nil
}

// Save 规范化整份列表后落盘(编辑器/GUI 入口;name 重复/非法 mode 显式失败)。
func Save(servers []Server) error {
	out := make([]Server, 0, len(servers))
	seen := map[string]bool{}
	for _, s := range servers {
		n, err := Normalize(s)
		if err != nil {
			return err
		}
		if seen[n.Name] {
			return fmt.Errorf("mcp: server 名 %q 重复", n.Name)
		}
		seen[n.Name] = true
		out = append(out, n)
	}
	return SaveFile(File{Servers: out})
}
