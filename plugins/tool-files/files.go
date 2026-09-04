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
	f := &FilesTool{sb: sb}
	return f.register(tools), nil
}

// FilesTool 文件工具集(单结构多工具定义由注册处展开)。
type FilesTool struct {
	sb sdk.Sandbox
}

// resolve 解析路径(相对 → workspace 根)并校验访问权。
func (f *FilesTool) resolve(path string, write bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("tool-files: 缺少 path 参数")
	}
	if f.sb == nil {
		return path, nil // 未装配沙箱:不限制(单测/独立部署)
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(f.sb.Root(), abs)
	}
	abs = filepath.Clean(abs)
	mode := f.sb.Mode()
	switch mode {
	case sdk.SandboxFullAccess:
		return abs, nil
	case sdk.SandboxReadOnly:
		if write {
			return "", fmt.Errorf("sandbox: read-only 拒绝写文件")
		}
		return abs, nil // 读允许
	default: // workspace-write:读写均限 workspace 内(防 ../ 穿越与读外泄)
		root := filepath.Clean(f.sb.Root())
		if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return "", fmt.Errorf("sandbox: workspace-write 拒绝访问 workspace 之外: %s", path)
		}
		return abs, nil
	}
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
		p, err := f.resolve(a.Path, false)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("读 %s: %v", p, err)}, nil
		}
		return map[string]any{"path": p, "content": string(b)}, nil
	case "file_write", "file_append":
		p, err := f.resolve(a.Path, true)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
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
		return map[string]any{"path": p, "wrote": len(a.Content)}, nil
	case "file_edit":
		p, err := f.resolve(a.Path, true)
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
		return map[string]any{"path": p, "replaced": 1}, nil
	}
	return map[string]any{"error": "未知文件操作 " + name}, nil
}

// register 注册四个工具(统一执行分发)。
func (f *FilesTool) register(tools sdk.ToolRegistry) sdk.Disposer {
	d1 := tools.Register(&fileTool{name: "file_read", f: f, schema: map[string]any{
		"type":     "object",
		"required": []any{"path"},
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
			"new" : map[string]any{"type": "string"},
		},
	}})
	return func() { d1(); d2(); d3(); d4() }
}

// fileTool 单个具名工具(定义与执行分发)。
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
	return sdk.ToolDefinition{Name: t.name, Description: desc, InputSchema: t.schema}
}

func (t *fileTool) Execute(ctx context.Context, raw string) (any, error) {
	return t.f.exec(ctx, t.name, raw)
}