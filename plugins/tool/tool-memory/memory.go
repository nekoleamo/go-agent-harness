// Package toolmemory 提供 tool-memory 插件(M10):跨会话操作记忆工具。
// 与 AGENTS.md 两层分工:AGENTS.md 承担用户手写静态偏好层;本工具补模型写操作层
// (决策/踩坑/偏好会话内沉淀 → 跨会话可取)。
//
// 存储:$GAH_HOME/memory/<project-key>.jsonl —— 追加式 jsonl,坏行容忍,人工可编辑
// (与 sessionlog 同三特性);按 project key 隔离(同 host-cwd-sessions 派生,见 ProjectKey)。
// 最小集显式拒绝过度工程:不做分层体系/闲时 LLM 自动提取/分块压缩。
package toolmemory

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

// Entry 一条记忆记录。jsonl 每行一个 Entry;同 id 以最后一行(最新写入)为准,
// Deleted=true 即废弃(墓碑行,保留可人工核对)。字段命名简短(省 token)。
type Entry struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Tags    []string `json:"tags,omitempty"`
	TS      int64    `json:"ts"`
	Deleted bool     `json:"del,omitempty"`
}

// Tool 实现 sdk.Tool(单工具多 action:remember/list/recall/forget)。
type Tool struct {
	store *Store
}

// NewTool 外部化工厂:外部进程入口的工具实例(GAH_HOME 贯通宿主与外部进程)。
func NewTool() sdk.Tool {
	return &Tool{store: NewStore("")}
}

// Plugin 实现 tool-memory(双轨:装配进宿主时经 ctx.tools 注册)。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-memory" }

// Start 注册 memory 工具。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	return tools.Register(NewTool()), nil
}

func (t *Tool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "memory",
		Description: "跨会话操作记忆(模型写操作层;AGENTS.md 为用户手写静态偏好,本工具存执行期" +
			"沉淀的决策/踩坑/偏好,人工可直接编辑 jsonl)。action 分发:" +
			"remember(text 必填,tags 可选)→ 存一条记忆;list(limit 默认10)→ 最近 N 条(预算限);" +
			"recall(query)→ 关键词检索 text/tags(大小写不敏感子串);forget(id)→ 废弃一条。跨会话可用:切换会话后仍可取回。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"action"},
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"remember", "list", "recall", "forget"}},
				"text":   map[string]any{"type": "string", "description": "remember:记忆内容"},
				"tags":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"limit":  map[string]any{"type": "integer", "description": "list/recall 条数上限"},
				"query":  map[string]any{"type": "string", "description": "recall 关键词"},
				"id":     map[string]any{"type": "string", "description": "forget 目标 id"},
			},
		},
	}
}

// Execute 执行一个 action;业务失败以 map{"error":...} 回传模型(不中断 turn)。
func (t *Tool) Execute(_ context.Context, raw string) (any, error) {
	var a struct {
		Action string   `json:"action"`
		Text   string   `json:"text"`
		Tags   []string `json:"tags"`
		Limit  int      `json:"limit"`
		Query  string   `json:"query"`
		ID     string   `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("memory: args: %w", err)
	}
	switch a.Action {
	case "remember":
		if strings.TrimSpace(a.Text) == "" {
			return map[string]any{"error": "memory: remember 需 text"}, nil
		}
		e, err := t.store.Remember(strings.TrimSpace(a.Text), a.Tags)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"id": e.ID, "saved": true}, nil
	case "list":
		return t.store.List(a.Limit), nil
	case "recall":
		if strings.TrimSpace(a.Query) == "" {
			return map[string]any{"error": "memory: recall 需 query"}, nil
		}
		return t.store.Recall(strings.TrimSpace(a.Query), a.Limit), nil
	case "forget":
		if strings.TrimSpace(a.ID) == "" {
			return map[string]any{"error": "memory: forget 需 id"}, nil
		}
		if err := t.store.Forget(strings.TrimSpace(a.ID)); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"forgotten": a.ID}, nil
	default:
		return map[string]any{"error": fmt.Sprintf("memory: 未知 action %q(remember|list|recall|forget)", a.Action)}, nil
	}
}

// Store 记忆存取:追加式 jsonl + 按 id 聚合(最后一行胜),坏行容忍,人工可编辑。
// Root 为空时取 $GAH_HOME/memory(缺省 ~/.gah/memory)。
type Store struct {
	mu   sync.Mutex
	root string // 测试注入 root;空 = 默认
}

// NewStore 构造存储;root 空 → 默认 $GAH_HOME/memory。
func NewStore(root string) *Store {
	return &Store{root: root}
}

// Remember 追加一条记忆,返回生成的 id。
func (s *Store) Remember(text string, tags []string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return Entry{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Entry{}, fmt.Errorf("memory: 创建目录: %w", err)
	}
	// id:时间戳 base36 + 同刻自增序号(防并发/同刻碰撞)
	seq := s.nextSeq(path)
	e := Entry{ID: fmt.Sprintf("%x-%d", time.Now().UnixNano(), seq),
		Text: text, Tags: tags, TS: time.Now().Unix(), Deleted: false}
	if err := appendLine(path, e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// nextSeq 当前文件追加行序号(读取计数后加一;仅供 id 唯一性)。
func (s *Store) nextSeq(path string) int64 {
	n := int64(0)
	if f, err := os.Open(path); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if len(sc.Bytes()) > 0 {
				n++
			}
		}
	}
	return n + 1
}

// List 最近 N 条未废弃记忆(时间倒序);n<=0 → 10(预算限),上限 50。
func (s *Store) List(n int) []Entry {
	if n <= 0 {
		n = 10
	}
	if n > 50 {
		n = 50
	}
	var alive []Entry
	for _, e := range s.load() {
		if !e.Deleted {
			alive = append(alive, e)
		}
	}
	return s.top(alive, n)
}

// Recall 关键词检索(大小写不敏感子串,匹配 text 与 tags),未废弃且命中。
func (s *Store) Recall(q string, n int) []Entry {
	if n <= 0 {
		n = 10
	}
	if n > 50 {
		n = 50
	}
	ql := strings.ToLower(q)
	var hit []Entry
	for _, e := range s.load() {
		if e.Deleted {
			continue
		}
		if strings.Contains(strings.ToLower(e.Text), ql) {
			hit = append(hit, e)
			continue
		}
		for _, t := range e.Tags {
			if strings.Contains(strings.ToLower(t), ql) {
				hit = append(hit, e)
				break
			}
		}
	}
	return s.top(hit, n)
}

// Forget 追加墓碑行废弃一条(id 不存在也记录——幂等;文本已废弃可由人工核对)。
func (s *Store) Forget(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("memory: 创建目录: %w", err)
	}
	return appendLine(path, Entry{ID: id, Deleted: true, TS: time.Now().Unix()})
}

// top 按时间倒序取前 n(结果拷贝,防调用方改内部态)。
// 同 TS(同秒写入)时后写入在前:先整体反序再稳定降序排 TS——
// 稳定排序保持组内反序(文件逆序 = 新→旧)。
func (s *Store) top(entries []Entry, n int) []Entry {
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].TS > entries[j].TS })
	if len(entries) > n {
		entries = entries[:n]
	}
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out
}

// load 全量重放:逐行解析(坏行跳过),同 id 最后一行胜(墓碑替换)。
func (s *Store) load() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil // 无文件 = 空
	}
	defer f.Close()
	byID := map[string]Entry{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil || e.ID == "" {
			continue // 坏行/手工注释行:容忍跳过
		}
		if _, seen := byID[e.ID]; !seen {
			order = append(order, e.ID)
		}
		byID[e.ID] = e
	}
	out := make([]Entry, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// path 记忆文件路径:root 注入 > $GAH_HOME/memory > ~/.gah/memory;<project-key>.jsonl。
func (s *Store) path() (string, error) {
	root := s.root
	if root == "" {
		root = filepath.Join(sdk.Home(), "memory")
	}
	return filepath.Join(root, sdk.ProjectKeyFromCwd()+".jsonl"), nil
}

// appendLine O_APPEND 追加一行(单行 JSON,含换行)。
func appendLine(path string, e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("memory: 编码: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("memory: 追加: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("memory: 写入: %w", err)
	}
	return nil
}
