// search.go:search 模式的两个代理工具(NOND-M1 第 2 步)。
//
// 动机:接一个 20 工具的 MCP server 时,全量工具定义会一直占着固定前缀(宿主每轮都下发
// schema)。search 模式把工具挪出注册表,模型只看得到 mcp_search / mcp_call 两个工具,
// 需要时先搜再调 —— 工具清单的代价从"每轮固定"变成"用到才付"。
//
// 可达集合的取舍:mcp_call **只**能调本索引里的工具(search 模式 server),不代调
// direct 模式工具 —— 保持"搜到的 = 能调的"一一对应,且不让工具名到审批规则
// (data.approval_tools 按名匹配)的映射被一个间接名绕过。
package mcpbridge

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 搜索返回条数上限(默认/硬上限)。
const (
	searchDefaultLimit = 20
	searchMaxLimit     = 100
)

// searchEntry 一条 search 模式工具索引。
type searchEntry struct {
	display string // 模型用名(mcp_<server>_<工具>)
	desc    string
	server  string
	conn    Conn
	raw     string // 连接内部名(交给 conn.Execute)
}

// searchIndex 索引(search 模式工具集合;装配期写入,运行期只读)。
type searchIndex struct {
	mu      sync.RWMutex
	entries []searchEntry
}

func (ix *searchIndex) add(e searchEntry) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.entries = append(ix.entries, e)
}

func (ix *searchIndex) len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.entries)
}

// find 按模型用名精确查找。
func (ix *searchIndex) find(name string) (searchEntry, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for _, e := range ix.entries {
		if e.display == name {
			return e, true
		}
	}
	return searchEntry{}, false
}

// search 打分检索:空 query = 全部;多词 = AND(每个词都要命中);按分数降序、同名升序稳定排序。
// 返回命中集合与总条数(供"已截断"提示)。
func (ix *searchIndex) search(query string, limit int) ([]searchEntry, int) {
	ix.mu.RLock()
	all := make([]searchEntry, len(ix.entries))
	copy(all, ix.entries)
	ix.mu.RUnlock()

	tokens := strings.Fields(strings.ToLower(query))
	type scored struct {
		e searchEntry
		s int
	}
	var hits []scored
	for _, e := range all {
		s := scoreEntry(e, tokens)
		if s <= 0 {
			continue
		}
		hits = append(hits, scored{e: e, s: s})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].s != hits[j].s {
			return hits[i].s > hits[j].s
		}
		return hits[i].e.display < hits[j].e.display
	})
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	if limit > searchMaxLimit {
		limit = searchMaxLimit
	}
	out := make([]searchEntry, 0, min(limit, len(hits)))
	for i, h := range hits {
		if i >= limit {
			break
		}
		out = append(out, h.e)
	}
	return out, len(hits)
}

// scoreEntry 打分(含义见 mcp_search 的 description;0 = 不命中):
// 名等值 100 / 名前缀 50 / 名包含 30 / 描述包含 10;每个 token 独立计分后相加。
// 有 token 完全不命中(名与描述都没有)= 不命中(AND 语义)。
func scoreEntry(e searchEntry, tokens []string) int {
	if len(tokens) == 0 {
		return 1 // 空查询 = 全量(顺序退化为名字升序)
	}
	name := strings.ToLower(e.display)
	desc := strings.ToLower(e.desc)
	total := 0
	for _, t := range tokens {
		switch {
		case name == t:
			total += 100
		case strings.HasPrefix(name, t), strings.Contains(name, "_"+t):
			total += 50
		case strings.Contains(name, t):
			total += 30
		case strings.Contains(desc, t):
			total += 10
		default:
			return 0 // 该 token 未命中 → 整条不命中
		}
	}
	return total
}

// newSearchTool mcp_search:列出/检索可用工具(空 query = 全量清单)。
func newSearchTool(ix *searchIndex) sdk.Tool { return &searchTool{ix: ix} }

type searchTool struct{ ix *searchIndex }

func (t *searchTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "mcp_search",
		Description: "搜索 MCP 工具清单(仅 search 模式的 MCP server)。返回工具的完整名字、所属 server 与用途;" +
			"命中后用 mcp_call 调用。query 留空 = 返回全部可用工具(用于总览)。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "检索词(空格分隔多词 = 全部词都要命中);留空返回全部"},
				"limit": map[string]any{"type": "integer", "description": "最多返回条数(默认 20,上限 100)"},
			},
			"required": []any{"query"},
		},
	}
}

func (t *searchTool) Execute(_ context.Context, args string) (any, error) {
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if strings.TrimSpace(args) != "" {
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			// 模型传非 JSON(裸词)时按检索词处理,不因参数格式失败
			in.Query = strings.TrimSpace(args)
		}
	}
	hits, total := t.ix.search(in.Query, in.Limit)
	list := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		item := map[string]any{"name": h.display, "description": h.desc}
		if h.server != "" {
			item["server"] = h.server
		}
		list = append(list, item)
	}
	out := map[string]any{
		"count": len(list),
		"total": total,
		"tools": list,
		"hint":  "用 mcp_call 调用:{\"name\": \"<上表 name>\", \"arguments\": {…该工具的 inputSchema…}}",
	}
	if len(list) < total {
		out["truncated"] = true
	}
	if total == 0 {
		out["hint"] = "没有匹配的工具(可用 query 留空看全部清单)"
	}
	return out, nil
}

// newCallTool mcp_call:按名调用 search 模式工具。
func newCallTool(ix *searchIndex) sdk.Tool { return &callTool{ix: ix} }

type callTool struct{ ix *searchIndex }

func (t *callTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "mcp_call",
		Description: "调用 search 模式的 MCP 工具。name 取 mcp_search 返回的名字(形如 mcp_<server>_<工具>);" +
			"arguments 为该工具的入参对象(见其 inputSchema)。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "description": "工具名(来自 mcp_search 结果)"},
				"arguments": map[string]any{"type": "object", "description": "该工具的入参(键值见其 inputSchema)"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *callTool) Execute(ctx context.Context, args string) (any, error) {
	var in struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return map[string]any{"error": "mcp_call 参数解析失败(需 {\"name\":…,\"arguments\":{…}}): " + err.Error()}, nil
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return map[string]any{"error": "mcp_call 缺少 name(先用 mcp_search 查可用工具)"}, nil
	}
	e, ok := t.ix.find(name)
	if !ok {
		if hit, found := t.ix.fuzzy(name); found {
			return map[string]any{"error": "未知 MCP 工具 " + name + "(你是想调 " + hit + " 吗?名字需与 mcp_search 返回的完全一致)"}, nil
		}
		return map[string]any{"error": "未知 MCP 工具 " + name + "(先用 mcp_search 查可用清单)"}, nil
	}
	payload := "{}"
	if raw := strings.TrimSpace(string(in.Arguments)); raw != "" && raw != "null" {
		// arguments 允许是对象或 JSON 字符串(模型两种都爱用)
		if strings.HasPrefix(raw, "\"") {
			var s string
			if json.Unmarshal(in.Arguments, &s) == nil && strings.TrimSpace(s) != "" {
				payload = strings.TrimSpace(s)
			}
		} else {
			payload = raw
		}
	}
	out, err := e.conn.Execute(ctx, e.raw, payload)
	if err != nil {
		return map[string]any{"error": "MCP 调用失败: " + err.Error()}, nil
	}
	return map[string]any{"content": out}, nil
}

// fuzzy 近似名提示(不改大小写无关匹配:只在未命中时给"是否想调 X"的线索)。
func (ix *searchIndex) fuzzy(name string) (string, bool) {
	low := strings.ToLower(name)
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for _, e := range ix.entries {
		if strings.ToLower(e.display) == low {
			return e.display, true
		}
	}
	return "", false
}
