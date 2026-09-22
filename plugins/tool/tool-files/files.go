// Package toolfiles 提供 tool-files 插件(M6.4):文件读写/编辑工具。
// 沙箱三档联动(sdk.Sandbox):写操作经 ValidatePath(read-only 拒绝、workspace-write
// 限 workspace 内、full 放行);读操作 read-only 允许、workspace-write 同样限
// workspace 内(防读外泄),相对路径一律以 workspace 根解析(防 ../ 穿越)。
package toolfiles

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 tool-files。requires ctx.tools;ctx.sandbox 可选(未装配则不限)。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-files" }

// Start 注册 file_read/file_write/file_append/file_edit 工具。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	var sb sdk.Sandbox
	_ = c.Inject("ctx.sandbox", &sb)
	f := &FilesTool{sb: sb, c: c}
	return f.register(tools), nil
}

// NewTools 外部化工厂(P1):四件套工具表(外部进程经桥协议暴露;沙箱由宿主注入,外部进程不装配)。
func NewTools() map[string]sdk.Tool { return NewToolsWith(nil) }

// NewToolsWith 带改动回传出口的外部化工厂:外部进程没有宿主 Ctx,写盘后的 file/change
// 事件经 rec(桥回传)交宿主落账 —— 否则默认发行态(工具已外部化)会**完全没有**变更记录,
// 变更视图/`/diff` 恒为空。rec 为 nil(无回调通道)时退回「无审计」但不静默(见 recordChange)。
func NewToolsWith(rec sdk.FileChangeRecorder) map[string]sdk.Tool {
	f := &FilesTool{sb: nil, rec: rec}
	return map[string]sdk.Tool{
		"file_read":   &fileTool{name: "file_read", f: f, schema: fileSchema("read")},
		"file_write":  &fileTool{name: "file_write", f: f, schema: fileSchema("write")},
		"file_append": &fileTool{name: "file_append", f: f, schema: fileSchema("write")},
		"file_edit":   &fileTool{name: "file_edit", f: f, schema: fileSchema("edit")},
	}
}

// fileSchema 工具参数 schema 模板。
func fileSchema(kind string) map[string]any {
	switch kind {
	case "read":
		return map[string]any{"type": "object", "required": []any{"path"},
			"properties": map[string]any{"path": map[string]any{"type": "string"}}}
	case "write":
		return map[string]any{"type": "object", "required": []any{"path", "content"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}}
	default:
		return map[string]any{"type": "object", "required": []any{"path", "old", "new"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"}, "old": map[string]any{"type": "string"},
				"new": map[string]any{"type": "string"}}}
	}
}

// FilesTool 文件工具集(单结构多工具定义由注册处展开)。
type FilesTool struct {
	sb sdk.Sandbox
	// c 宿主上下文(S-P1-1 审计用):ctx.sessions 在**写盘时**现取 —— 不依赖插件启动顺序
	// (与 /recap 等命令同一口径);外部进程工厂下为 nil。
	c sdk.Ctx
	// rec 外部进程的改动回传出口(外部进程没有宿主 Ctx):c == nil 时用它把 file/change
	// 事件交宿主落账;两者皆无 = 无审计(仅内嵌未装配账本/裸进程直跑会出现)。
	rec sdk.FileChangeRecorder
}

// resolve 解析路径(相对 → 本次调用工作根)并校验访问权。
// 相对路径基准优先级:本次调用工作根(宿主经 sdk.SandboxHint.Root 下传;隔离子代理 = 受管
// worktree)> 沙箱 workspace 根。**这是隔离能生效的关键**:外部插件进程的 cwd 恒为主工作区,
// 按进程 cwd 解析会把子代理的写落回主工作区(两子代理继续互相覆盖)。
// 校验统一委托沙箱(唯一裁决点):写经 ValidatePath、读经 ValidateRead(可选能力);
// 沙箱实现 RootScoped(sdk)时按**同一工作根**校验,否则退回自身 root(未隔离调用行为不变)。
// 未实现读校验能力时退回本插件三档兜底(单测/独立部署场景)。
func (f *FilesTool) resolve(ctx context.Context, path string, write bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("tool-files: 缺少 path 参数")
	}
	// URL 不是路径:模型偶发把网页地址交给文件工具(真机:工作目录里长出 https:/host/docs/… 空目录树)。
	// 沙箱层(policy-guard)也拦;这里再拦一道,是因为**未装配沙箱**时(单测/独立部署)resolve 会直接放行。
	if sdk.LooksLikeURLPath(path) {
		return "", fmt.Errorf("tool-files: path 是 URL 而不是本地文件路径: %s(抓网页请用 web 工具)", path)
	}
	base := ""
	if h, ok := sdk.SandboxHintOf(ctx); ok {
		base = h.Root
	}
	if base == "" && f.sb != nil {
		base = f.sb.Root()
	}
	abs := path
	if !filepath.IsAbs(abs) && base != "" {
		abs = filepath.Join(base, abs)
	}
	abs = filepath.Clean(abs)
	if f.sb == nil {
		return abs, nil // 未装配沙箱:不限制(单测/独立部署;宿主侧 pre-execute 仍会裁决)
	}
	if write {
		if sc, ok := f.sb.(sdk.RootScoped); ok {
			return abs, sc.ValidatePathAt(base, abs)
		}
		return abs, f.sb.ValidatePath(abs)
	}
	if rv, ok := f.sb.(sdk.ReadValidator); ok {
		if sc, ok2 := f.sb.(sdk.RootScoped); ok2 {
			return abs, sc.ValidateReadAt(base, abs)
		}
		return abs, rv.ValidateRead(abs)
	}
	// 兜底:沙箱未实现读校验能力 → 按**有效档位**判定(联动开启时 Mode() 不代表拦截行为)
	switch effectiveSandboxMode(f.sb) {
	case sdk.SandboxFullAccess, sdk.SandboxReadOnly:
		return abs, nil // 读放行
	default: // workspace-write:读限 workspace 内(防 ../ 穿越与读外泄)
		root := filepath.Clean(base)
		if root == "" {
			root = filepath.Clean(f.sb.Root())
		}
		if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return "", fmt.Errorf("sandbox: workspace-write 拒绝访问 workspace 之外: %s", path)
		}
		return abs, nil
	}
}

// effectiveSandboxMode 优先取联动后的有效档(沙箱实现 sdk.EffectiveSandbox 时)。
// 否则 policy-guard 的档位联动(sync)会被工具侧忽略:approval=strict 显示只读、
// 工具仍按 workspace-write 放行。
func effectiveSandboxMode(sb sdk.Sandbox) sdk.SandboxMode {
	if es, ok := sb.(sdk.EffectiveSandbox); ok {
		return es.EffectiveMode()
	}
	return sb.Mode()
}

// exec 统一工具执行(按工具名分发)。
func (f *FilesTool) exec(ctx context.Context, name, raw string) (any, error) {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content,omitempty"`
		Old     string `json:"old,omitempty"`
		New     string `json:"new,omitempty"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("%s: args: %w", name, err)
	}
	switch name {
	case "file_read":
		p, err := f.resolve(ctx, a.Path, false)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("读 %s: %v", p, err)}, nil
		}
		return map[string]any{"path": p, "content": string(b)}, nil
	case "file_write", "file_append":
		p, err := f.resolve(ctx, a.Path, true)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		before, existed := readForDiff(p)
		flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if name == "file_append" {
			flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return map[string]any{"error": fmt.Sprintf("建目录 %s: %v", filepath.Dir(p), err)}, nil
		}
		fh, err := os.OpenFile(p, flags, 0o644)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("打开 %s: %v", p, err)}, nil
		}
		if _, err := fh.WriteString(a.Content); err != nil {
			fh.Close()
			return map[string]any{"error": fmt.Sprintf("写入 %s: %v", p, err)}, nil
		}
		fh.Close()
		op := "write"
		after := a.Content
		if name == "file_append" {
			op = "append"
			// 追加后的真实内容 = 原内容 + 追加段(before 为空 = 新建)
			after = before + a.Content
		}
		f.recordChange(p, op, name, before, existed, after)
		return map[string]any{"path": p, "wrote": len(a.Content)}, nil
	case "file_edit":
		p, err := f.resolve(ctx, a.Path, true)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		if a.Old == "" {
			return map[string]any{"error": "file_edit: 需要 old 片段"}, nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("读 %s: %v", p, err)}, nil
		}
		s := string(b)
		if !strings.Contains(s, a.Old) {
			return map[string]any{"error": fmt.Sprintf("file_edit: %s 中没有匹配片段", p)}, nil
		}
		out := strings.ReplaceAll(s, a.Old, a.New)
		if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
			return map[string]any{"error": fmt.Sprintf("写 %s: %v", p, err)}, nil
		}
		f.recordChange(p, "edit", name, s, true, out)
		return map[string]any{"path": p, "replaced": 1}, nil
	}
	return map[string]any{"error": "未知文件操作 " + name}, nil
}

// readForDiff 读写前内容供审计(existed=false 表示目标原不存在;读失败也按不存在处理,
// 写盘本身会在后续报错——审计不参与写盘成败判定)。
func readForDiff(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// recordChange 写盘成功后追加 file/change 事件(S-P1-1 变更审查面)。
// 审计失败不得改变写盘结果(写已经落盘了) —— 只记日志,不报错、不静默。
// 两条路:内嵌(有宿主 Ctx → 直接 Append 账本)/ 外部进程(经 rec 桥回传给宿主落账)。
func (f *FilesTool) recordChange(path, op, tool, before string, existed bool, after string) {
	if before == after {
		return // 内容未变(重写同名内容不必刷空事件)
	}
	ev := sdk.BuildFileChange(path, f.relPath(path), op, tool, !existed, before, after)
	if f.c != nil { // 内嵌:宿主 Ctx 现取账本(不依赖插件启动顺序)
		var sess sdk.SessionLog
		if err := f.c.Inject("ctx.sessions", &sess); err != nil || sess == nil {
			return // 未装配账本(极简 profile):审计整体不可用,不影响写盘
		}
		if err := sess.Append(sdk.SessionEvent{Kind: sdk.EventFileChange, Payload: ev}); err != nil {
			if log := f.c.Logger(); log != nil {
				log.Warn("file/change 事件记录失败(改动已落盘)", "path", path, "err", err)
			}
		}
		return
	}
	if f.rec != nil { // 外部进程:交宿主落账(Rel 由宿主按其沙箱根补算)
		if err := f.rec.RecordChange(ev); err != nil {
			fmt.Fprintf(os.Stderr, "tool-files: file/change 回传失败(改动已落盘): %v\n", err)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "tool-files: 无改动记录通道(宿主 Ctx 与桥回调皆无),%s 的改动未入账\n", path)
}

// relPath 相对工作区路径(展示/聚合主键;无沙箱或不在工作区内时回退空串)。
func (f *FilesTool) relPath(p string) string {
	if f.sb == nil || f.sb.Root() == "" {
		return ""
	}
	root := filepath.Clean(f.sb.Root())
	rel, err := filepath.Rel(root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// register 注册四个工具(统一执行分发)。
func (f *FilesTool) register(tools sdk.ToolRegistry) sdk.Disposer {
	d1 := tools.Register(&fileTool{name: "file_read", f: f, schema: map[string]any{
		"type":       "object",
		"required":   []any{"path"},
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
	}})
	d2 := tools.Register(&fileTool{name: "file_write", f: f, schema: map[string]any{
		"type":     "object",
		"required": []any{"path", "content"},
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		},
	}})
	d3 := tools.Register(&fileTool{name: "file_append", f: f, schema: map[string]any{
		"type":     "object",
		"required": []any{"path", "content"},
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		},
	}})
	d4 := tools.Register(&fileTool{name: "file_edit", f: f, schema: map[string]any{
		"type":     "object",
		"required": []any{"path", "old", "new"},
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
			"old":  map[string]any{"type": "string"},
			"new":  map[string]any{"type": "string"},
		},
	}})
	return func() { d1(); d2(); d3(); d4() }
}

// fileTool 单个具名工具(定义与执行分发)。
// fileAccess 工具名的读写意图(file_read 读;其余改写)。
func fileAccess(name string) sdk.PathAccess {
	if name == "file_read" {
		return sdk.PathRead
	}
	return sdk.PathWrite
}

type fileTool struct {
	name   string
	f      *FilesTool
	schema map[string]any
}

func (t *fileTool) Definition() sdk.ToolDefinition {
	desc := map[string]string{
		"file_read":   "读文件内容:{path}",
		"file_write":  "写文件(覆盖):{path, content}",
		"file_append": "追加文件:{path, content}",
		"file_edit":   "替换文件中的片段:{path, old, new}"}[t.name]
	return sdk.ToolDefinition{
		Name: t.name, Description: desc, InputSchema: t.schema,
		// 路径参数能力声明(宿主 pre-execute 路径沙箱据此裁决;外部插件经桥协议原样传递)
		PathParams: []sdk.PathParam{{Arg: "path", Access: fileAccess(t.name)}},
	}
}

func (t *fileTool) Execute(ctx context.Context, raw string) (any, error) {
	return t.f.exec(ctx, t.name, raw)
}
