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

// Start 注册 file_read/file_write/file_append/file_edit 与 F1 文件操作工具(M1 十个动作)。
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

// NewTools 外部化工厂(P1):四件套 + F1 文件操作工具表(外部进程经桥协议暴露;沙箱由宿主注入,外部进程不装配)。
func NewTools() map[string]sdk.Tool { return NewToolsWith(nil) }

// NewToolsWith 带改动回传出口的外部化工厂:外部进程没有宿主 Ctx,写盘后的 file/change
// 事件经 rec(桥回传)交宿主落账 —— 否则默认发行态(工具已外部化)会**完全没有**变更记录,
// 变更视图/`/diff` 恒为空。rec 为 nil(无回调通道)时退回「无审计」但不静默(见 recordChange)。
func NewToolsWith(rec sdk.FileChangeRecorder) map[string]sdk.Tool {
	f := &FilesTool{sb: nil, rec: rec}
	tools := map[string]sdk.Tool{
		"file_read":   &fileTool{name: "file_read", f: f, schema: fileSchema("read")},
		"file_write":  &fileTool{name: "file_write", f: f, schema: fileSchema("write")},
		"file_append": &fileTool{name: "file_append", f: f, schema: fileSchema("write")},
		"file_edit":   &fileTool{name: "file_edit", f: f, schema: fileSchema("edit")},
	}
	for _, n := range opsNames { // F1:与内嵌注册同一份定义(ops.go 的 schema/PathParams/描述)
		tools[n] = &fileTool{name: n, f: f, schema: opsSchema(n)}
	}
	return tools
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
// 校验委托 sdk.ResolveSandboxPath(**写目标裁决的唯一实现**,tool-image 等工具共用同一口径);
// 未实现读校验能力时由该函数按有效档位兜底(单测/独立部署场景)。
func (f *FilesTool) resolve(ctx context.Context, path string, write bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("tool-files: 缺少 path 参数")
	}
	// URL 不是路径:模型偶发把网页地址交给文件工具(真机:工作目录里长出 https:/host/docs/… 空目录树)。
	// 沙箱层(policy-guard)也拦;这里再拦一道,是因为**未装配沙箱**时(单测/独立部署)resolve 会直接放行。
	if sdk.LooksLikeURLPath(path) {
		return "", fmt.Errorf("tool-files: path 是 URL 而不是本地文件路径: %s(抓网页请用 web 工具)", path)
	}
	return sdk.ResolveSandboxPath(ctx, f.sb, path, write)
}

// exec 统一工具执行(按工具名分发)。
func (f *FilesTool) exec(ctx context.Context, name, raw string) (any, error) {
	// F1 文件操作走自己的分派(入参形状不同:from/to/paths/limit…),代码在 ops.go
	if isOpsTool(name) {
		return f.execOps(ctx, name, raw)
	}
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
	f.emitChange(ev, path)
}

// emitChange file/change 事件的两条出口(内嵌 Ctx / 外部进程桥)—— recordChange 与 recordRaw 共用。
func (f *FilesTool) emitChange(ev sdk.FileChangeEvent, path string) {
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

// register 注册工具(统一执行分发):四件套 + F1 文件操作。
func (f *FilesTool) register(tools sdk.ToolRegistry) sdk.Disposer {
	var ds []func()
	ds = append(ds, tools.Register(&fileTool{name: "file_read", f: f, schema: map[string]any{
		"type":       "object",
		"required":   []any{"path"},
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
	}}))
	ds = append(ds, tools.Register(&fileTool{name: "file_write", f: f, schema: map[string]any{
		"type":     "object",
		"required": []any{"path", "content"},
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		},
	}}))
	ds = append(ds, tools.Register(&fileTool{name: "file_append", f: f, schema: map[string]any{
		"type":     "object",
		"required": []any{"path", "content"},
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		},
	}}))
	ds = append(ds, tools.Register(&fileTool{name: "file_edit", f: f, schema: map[string]any{
		"type":     "object",
		"required": []any{"path", "old", "new"},
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
			"old":  map[string]any{"type": "string"},
			"new":  map[string]any{"type": "string"},
		},
	}}))
	// F1 文件操作(ops.go 持有各自 schema/PathParams/描述)
	for _, n := range opsNames {
		ds = append(ds, tools.Register(&fileTool{name: n, f: f, schema: opsSchema(n)}))
	}
	return func() {
		for _, d := range ds {
			d()
		}
	}
}

// fileTool 单个具名工具(定义与执行分发)。
// fileAccess 工具名的读写意图(file_read 读;F1 十个动作按 opsPathParams 声明)。
func fileAccess(name string) sdk.PathAccess {
	if name == "file_read" || isOpsTool(name) {
		return sdk.PathRead
	}
	return sdk.PathWrite
}

type fileTool struct {
	name   string
	f      *FilesTool
	schema map[string]any
}

// fileOpDescs 四件套描述;F1 十个动作的描述在 ops.go(opsDesc)。
var fileOpDescs = map[string]string{
	"file_read":   "读文件内容:{path}",
	"file_write":  "写文件(覆盖):{path, content}",
	"file_append": "追加文件:{path, content}",
	"file_edit":   "替换文件中的片段:{path, old, new}",
}

func (t *fileTool) Definition() sdk.ToolDefinition {
	desc := fileOpDescs[t.name]
	if desc == "" {
		desc = opsDesc(t.name)
	}
	params := opsPathParams(t.name)
	if params == nil {
		// 四件套只有单个 path 参数;F1 各动作的声明在 ops.go
		params = []sdk.PathParam{{Arg: "path", Access: fileAccess(t.name)}}
	}
	return sdk.ToolDefinition{
		Name: t.name, Description: desc, InputSchema: t.schema,
		// 路径参数能力声明(宿主 pre-execute 路径沙箱据此裁决;外部插件经桥协议原样传递)
		PathParams: params,
	}
}

func (t *fileTool) Execute(ctx context.Context, raw string) (any, error) {
	return t.f.exec(ctx, t.name, raw)
}
