// Package hostcwdsessions 提供 host-cwd-sessions 插件:项目级会话隔离与持久化,
// 支持多会话切换(同一项目可开多个会话,继续任一历史)。
// 会话落盘 $GAH_HOME/sessions/:主会话 <key>.jsonl(跨期共享,兼容旧版);
// 切换会话 <key>-<id>.jsonl(id = 创建时间戳)。每次启动即新开会话(空历史、新文件),
// 过往对话保留在主会话/历史切换会话文件,经 TUI /session switch 回溯。
// 不同项目隔离(对齐 dsc 项目式历史隔离)。
package hostcwdsessions

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-cwd-sessions。requires ctx.sessions。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-cwd-sessions" }

// Start 注入会话日志,启动即新开会话(空历史、新落盘文件)并注册 ctx.cwdSessions。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		return nil, err
	}
	key := ProjectKeyFromCwd()
	svc := &Service{key: key, sessions: sessions}
	// 工作区切换事件广播(广播模式,监听器错误仅记日志不中断切换)
	svc.emitWS = func(dir string) {
		_, _ = c.Emit(context.Background(), "cwd/workspace-switched", dir, sdk.Emit)
	}
	// 会话切换事件广播(Open/New 后;UI 订阅重放,B3 命令下沉双端联动)
	svc.emitSession = func(id string) {
		_, _ = c.Emit(context.Background(), "cwd/session-switched", id, sdk.Emit)
	}
	svc.recordProject(key, currentDir()) // 启动即记录当前项目(最近使用列表)
	// 每次启动 = 新会话(空历史):不再自动恢复主会话——过往对话保留在
	// <key>.jsonl(主会话)/历史切换会话文件,经 /session switch 回溯。
	if _, err := svc.New(); err != nil {
		return nil, err
	}
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

// SessionPath 会话落盘路径(纯函数):id 空 = 主会话 <key>.jsonl;否则 <key>-<id>.jsonl。
func SessionPath(root, key, id string) string {
	if id == "" {
		return filepath.Join(root, key+".jsonl")
	}
	return filepath.Join(root, key+"-"+id+".jsonl")
}

// Service 实现 sdk.CwdSessions。
type Service struct {
	key      string
	path     string         // 当前会话落盘路径
	current  string         // 当前会话 id(空 = 主会话)
	sessions sdk.SessionLog // ctx.sessions(切换时 Load 恢复历史)
	wsMu     sync.Mutex     // workspaces 记录文件写锁
	nmMu     sync.Mutex     // 会话显示名(names.json)读写锁
	ftMu     sync.Mutex     // 分支树衍生记录(fork-tree.json)读写锁
	// emitWS 工作区切换事件广播(可选,nil = 不广播;Plugin.Start 绑定 c.Emit)。
	// 宿主订阅方:host-bridge(重启外部工具进程使其继承新 cwd)、
	// policy-guard(沙箱 root 同步)——工具真正在新目录执行。
	emitWS func(dir string)

	// emitSession 会话切换事件广播(可选;Open/New 后触发,UI 订阅重放刷新)。
	emitSession func(id string)
}

func (s *Service) Current() string        { return s.key }
func (s *Service) Path() string           { return s.path }
func (s *Service) CurrentSession() string { return s.current }

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

// Sessions 当前项目的会话列表:主会话置顶,切换会话按最后修改时间倒序。
// 每条带落盘路径/修改时间/事件数(TUI 选择器展示)。
func (s *Service) Sessions() []sdk.SessionInfo {
	root := SessionsRoot()
	s.nmMu.Lock()
	names := loadNames(filepath.Join(root, "names.json"))
	s.nmMu.Unlock()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	prefix := s.key + "-"
	var out []sdk.SessionInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		var id string
		switch {
		case name == s.key+".jsonl":
			id = ""
		case strings.HasPrefix(name, prefix):
			id = strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".jsonl")
		default:
			continue // 其他项目会话
		}
		info := sdk.SessionInfo{ID: id, Path: filepath.Join(root, name), Name: names[name]}
		if fi, err := e.Info(); err == nil {
			info.MTime = fi.ModTime().Unix()
			info.Frames = countLines(info.Path)
		}
		info.Preview = previewOf(info.Path, 48) // 内容省略版(首条用户消息截断)
		out = append(out, info)
	}
	// 主会话(id 空)置顶;切换会话按修改时间倒序(最近在前)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ID == "" {
			return true
		}
		if out[j].ID == "" {
			return false
		}
		return out[i].MTime > out[j].MTime
	})
	return out
}

// Open 切换当前会话:id 空 = 主会话;否则载入 <key>-<id>.jsonl。
// 文件不存在 = 新建会话(空历史)。切换后历史经 ctx.sessions.Load 恢复,后续续记。
func (s *Service) Open(id string) error {
	path := SessionPath(SessionsRoot(), s.key, id)
	if s.sessions != nil {
		if err := s.sessions.Load(path); err != nil {
			return err
		}
	}
	s.path = path
	s.current = id
	if s.emitSession != nil {
		s.emitSession(id)
	}
	return nil
}

// New 新建会话:生成唯一 id(时间戳;同分钟冲突追加序号)并 Open,返回新会话 id。
func (s *Service) New() (string, error) {
	base := time.Now().Format("20060102-150405")
	id := base
	for n := 2; ; n++ {
		if !fileExists(SessionPath(SessionsRoot(), s.key, id)) {
			break
		}
		id = fmt.Sprintf("%s-%d", base, n)
		if n > 10000 {
			return "", fmt.Errorf("cwdsessions: 无法生成唯一会话 id(%s)", base)
		}
	}
	if err := s.Open(id); err != nil {
		return "", err
	}
	return id, nil
}

// Delete 删除会话记录:仅删 <key>[-<id>].jsonl 与显示名索引条目不碰任何目录;
// id 空 = 主会话;删除的是当前打开会话时自动新建空会话承接(删除即干净新起点,
// 不回主会话——避免旧历史立刻回放造成“没删干净”观感)。
func (s *Service) Delete(id string) error {
	if !validSessionID(id) {
		return fmt.Errorf("cwdsessions: 非法会话 id")
	}
	file := s.key + ".jsonl"
	if id != "" {
		file = s.key + "-" + id + ".jsonl"
	}
	path := filepath.Join(SessionsRoot(), file)
	if !fileExists(path) {
		return fmt.Errorf("cwdsessions: 会话不存在: %s", file)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	// 清理显示名索引(非关键路径,失败容忍)
	s.nmMu.Lock()
	m := loadNames(sessionNamesPath())
	if _, ok := m[file]; ok {
		delete(m, file)
		_ = saveNames(sessionNamesPath(), m)
	}
	s.nmMu.Unlock()
	// 删除的若是当前打开会话 → 新建空会话承接
	if s.current == id {
		_, err := s.New()
		return err
	}
	return nil
}

// UnrecordProject 删除工作区使用记录:仅从 workspaces 记录移除该 key,
// 不删除对应文件夹与其中的会话文件;不存在幂等成功。
func (s *Service) UnrecordProject(key string) error {
	if key == "" {
		return fmt.Errorf("cwdsessions: 非法工作区 key")
	}
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	recs := loadWorkspaces(workspacesPath())
	out := recs[:0]
	for _, r := range recs {
		if r.Key != key {
			out = append(out, r)
		}
	}
	if len(out) == len(recs) {
		return nil // 无变更(不存在即幂等)
	}
	return saveWorkspaces(workspacesPath(), out)
}

// Rename 设置当前会话显示名(name 空 = 清除,展示回退 id/主会话)。
// 名写入 names.json(覆写式索引,key = 会话文件名),随会话文件持久,
// 重启/切会话仍保留;非关键路径,写失败静默容忍(同 workspaces)。
func (s *Service) Rename(name string) error {
	s.nmMu.Lock()
	defer s.nmMu.Unlock()
	m := loadNames(sessionNamesPath())
	if m == nil {
		m = map[string]string{}
	}
	file := filepath.Base(s.path)
	if name == "" {
		delete(m, file)
	} else {
		m[file] = name
	}
	return saveNames(sessionNamesPath(), m)
}

// SessionName 当前会话显示名(空 = 未命名)。
func (s *Service) SessionName() string {
	s.nmMu.Lock()
	defer s.nmMu.Unlock()
	m := loadNames(sessionNamesPath())
	if m == nil {
		return ""
	}
	return m[filepath.Base(s.path)]
}

// SwitchProject 切换当前项目(key 重绑):
// /workspace 后由宿主 os.Chdir 再调本方法(新 key = sdk.ProjectKeyFromCwd)。
// 重绑后自动新建空会话(上下文与后续记录切新项目文件;旧项目经 /session switch 回溯)。
// 无论是否同 key 均刷新该项目的“最近使用”时间(记录文件);同 key 仅 touch 不建新会话。
// 调用方串行(回合外)。
func (s *Service) SwitchProject(key string) (string, error) {
	if key == "" {
		key = "default"
	}
	s.recordProject(key, currentDir())
	if key == s.key {
		return s.current, nil // 同项目:仅刷新最近使用时间
	}
	s.key = key
	if s.emitWS != nil {
		s.emitWS(currentDir()) // 同 SwitchDir:广播通知宿主同步
	}
	return s.New()
}

// SwitchDir 切换工作区到真实目录(dir 语义,对齐 TUI /workspace):
// os.Chdir(dir) → key = sdk.ProjectKey(dir) → 重绑并新建空会话。
// 工作区记录以真实 dir 落盘(修复 web 端按 key 切换导致的 dir 污染:
// 不再依赖宿主当前 cwd 猜目录)。目录不可用显式失败(不静默降级)。
func (s *Service) SwitchDir(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("cwdsessions: 缺工作区目录")
	}
	if err := os.Chdir(dir); err != nil {
		return "", fmt.Errorf("cwdsessions: 工作区目录不可用 %s: %w", dir, err)
	}
	key := sdk.ProjectKey(dir)
	s.recordProject(key, dir) // 真实目录(不随宿主 cwd 漂移)
	if key == s.key {
		return s.current, nil // 同项目:仅刷新最近使用时间
	}
	s.key = key
	if s.emitWS != nil {
		s.emitWS(dir) // 通知宿主重启外部工具进程/同步沙箱 root,使真 cwd 生效
	}
	return s.New()
}

// RecentProjects 最近使用工作区列表(按最近使用时间倒序;无记录 = 空)。
func (s *Service) RecentProjects() []sdk.ProjectInfo {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	recs := loadWorkspaces(workspacesPath())
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].TS > recs[j].TS })
	return recs
}

// recordProject 幂等 upsert 一条最近使用记录(量小,覆写式 json)。
func (s *Service) recordProject(key, dir string) {
	if key == "" || dir == "" {
		return
	}
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	recs := loadWorkspaces(workspacesPath())
	now := time.Now().Unix()
	for i := range recs {
		if recs[i].Key == key {
			recs[i].Dir, recs[i].TS = dir, now
			_ = saveWorkspaces(workspacesPath(), recs)
			return
		}
	}
	recs = append(recs, sdk.ProjectInfo{Key: key, Dir: dir, TS: now})
	_ = saveWorkspaces(workspacesPath(), recs)
}

// workspacesPath 最近使用工作区记录文件($GAH_HOME/sessions/workspaces.json)。
func workspacesPath() string {
	return filepath.Join(SessionsRoot(), "workspaces.json")
}

// sessionNamesPath 会话显示名索引文件($GAH_HOME/sessions/names.json)。
// 与 workspaces.json 同目录同类覆写式 JSON;key = 会话文件名(<key>.jsonl /
// <key>-<id>.jsonl,全局唯一),value = 显示名。
func sessionNamesPath() string {
	return filepath.Join(SessionsRoot(), "names.json")
}

// loadNames 读显示名索引(缺文件/坏 json = 空 map,容忍;与 workspaces 同款)。
func loadNames(path string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

// saveNames 覆写显示名索引(目录自动建;失败静默——命名非关键路径)。
func saveNames(path string, m map[string]string) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// loadWorkspaces 读记录(缺文件/坏 json = 空,容忍)。
func loadWorkspaces(path string) []sdk.ProjectInfo {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var recs []sdk.ProjectInfo
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil
	}
	return recs
}

// saveWorkspaces 覆写记录(目录自动建;失败静默——记录非关键路径)。
func saveWorkspaces(path string, recs []sdk.ProjectInfo) error {
	b, err := json.Marshal(recs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// currentDir 当前工作目录(记录用;取不到 = 空,跳过记录)。
func currentDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// countLines 文件事件条数(会话描述用;读不了 = -1)。
// previewOf 会话内容省略版:扫描 jsonl 取首条用户消息(user/message)文本,
// 截断到 maxRunes(超限加省略号);无用户消息/坏行 = 空。仅读第一条即停,廉价。
func previewOf(path string, maxRunes int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var ev struct {
			Kind    string
			Payload map[string]any
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Kind != "user/message" {
			continue
		}
		s, _ := ev.Payload["Content"].(string)
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		r := []rune(s)
		if len(r) > maxRunes {
			return string(r[:maxRunes]) + "…"
		}
		return s
	}
	return ""
}

// validSessionID 会话 id 字符白名单(防路径穿越:仅字母数字与 -_)。
func validSessionID(id string) bool {
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '_':
			continue
		default:
			return false
		}
	}
	return true
}

func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) > 0 {
			n++
		}
	}
	return n
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
