// Package hostcwdsessions 提供 host-cwd-sessions 插件:项目级会话隔离与持久化。
// 按 cwd 派生项目 key,会话落盘 $GAH_HOME/sessions/<key>.jsonl(GAH_HOME 缺省 ~/.gah);
// 同项目跨期共享历史,不同项目隔离(对齐 dsc 项目式历史隔离)。重启后经同一 key 恢复。
package hostcwdsessions

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-cwd-sessions。requires ctx.sessions。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-cwd-sessions" }

// Start 注入会话日志并设置项目级落盘路径;注册 ctx.cwdSessions。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		return nil, err
	}
	key := ProjectKeyFromCwd()
	path := filepath.Join(SessionsRoot(), key+".jsonl")
	sessions.SetPath(path)
	svc := &Service{key: key, path: path}
	if err := c.Provide("ctx.cwdSessions", svc); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// SessionsRoot $GAH_HOME/sessions(缺省 ~/.gah/sessions)。
func SessionsRoot() string {
	home := os.Getenv("GAH_HOME")
	if home == "" {
		uh, err := os.UserHomeDir()
		if err != nil {
			uh = os.TempDir()
		}
		home = filepath.Join(uh, ".gah")
	}
	return filepath.Join(home, "sessions")
}

// ProjectKeyFromCwd 当前工作目录 → 项目 key(绝对路径清洗)。
func ProjectKeyFromCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "default"
	}
	return ProjectKey(wd)
}

// ProjectKey 绝对路径 → 项目 key:分隔符/冒号统一转 -,去除首尾 -。
func ProjectKey(abs string) string {
	clean := filepath.Clean(abs)
	if clean == "." || clean == "" {
		return "default"
	}
	r := strings.NewReplacer("/", "-", `\`, "-", ":", "-")
	out := strings.Trim(r.Replace(clean), "-")
	if out == "" {
		return "default"
	}
	return out
}

// Service 实现 sdk.CwdSessions。
type Service struct {
	key  string
	path string
}

func (s *Service) Current() string { return s.key }
func (s *Service) Path() string    { return s.path }

// List 列出 sessions 目录下已有项目会话 key(按名称排序)。
func (s *Service) List() []string {
	entries, err := os.ReadDir(SessionsRoot())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".jsonl") {
			out = append(out, strings.TrimSuffix(name, ".jsonl"))
		}
	}
	sort.Strings(out)
	return out
}

var _ = fmt.Sprintf // 保留 fmt(潜在调试)
