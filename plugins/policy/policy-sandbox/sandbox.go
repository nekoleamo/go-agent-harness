// Package policysandbox 提供 policy-sandbox 插件:ctx.sandbox 服务 + tools/pre-execute 拦截。
// 三档:read-only(veto 解释器/执行器类工具)/ workspace-write(默认,路径校验)/ full-access(放行)。
package policysandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 policy-sandbox。
type Plugin struct{}

func (p *Plugin) Name() string { return "policy-sandbox" }

// Start 注册 ctx.sandbox 服务并挂 pre-execute 拦截。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	mode := sdk.SandboxWorkspace
	if m != nil && m.Data != nil {
		if md, ok := m.Data["mode"].(string); ok && md != "" {
			mode = sdk.SandboxMode(md)
		}
	}
	s := &Policy{root: workspaceRoot()}
	s.SetMode(mode)

	if err := c.Provide("ctx.sandbox", s); err != nil {
		return nil, err
	}
	// pre-execute:veto 拦截(返回错误 → 工具被阻止,结构化错误回传模型)
	d := c.Subscribe("tools/pre-execute", func(ctx context.Context, ev *sdk.Event) error {
		call, ok := ev.Payload.(*sdk.ToolCallEvent)
		if !ok {
			return nil
		}
		return s.CheckTool(call.Name)
	})
	// 工作区切换事件:沙箱 root 同步到新目录(写校验/相对根解析即时生效)
	d2 := c.Subscribe("cwd/workspace-switched", func(ctx context.Context, ev *sdk.Event) error {
		if dir, ok := ev.Payload.(string); ok {
			s.SetRoot(dir)
		}
		return nil
	})
	return func() {
		d()
		d2()
	}, nil
}

// Policy 实现 sdk.Sandbox。
type Policy struct {
	mu   sync.RWMutex
	mode sdk.SandboxMode
	root string
}

func (p *Policy) Mode() sdk.SandboxMode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

func (p *Policy) SetMode(m sdk.SandboxMode) {
	p.mu.Lock()
	p.mode = m
	p.mu.Unlock()
}

func (p *Policy) Root() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.root
}

// SetRoot 更新 workspace 根(工作区切换后调用;读/写校验即时按新 root)。
func (p *Policy) SetRoot(dir string) {
	if dir == "" {
		return
	}
	p.mu.Lock()
	p.root = filepath.Clean(dir)
	p.mu.Unlock()
}

// ValidatePath 路径写校验(read-only 拒绝一切;workspace-write 限制在 root 内,防 ../ 穿越)。
func (p *Policy) ValidatePath(path string) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	switch p.mode {
	case sdk.SandboxReadOnly:
		return fmt.Errorf("sandbox: read-only 拒绝任何写操作")
	case sdk.SandboxFullAccess:
		return nil
	default: // workspace-write(相对路径一律以 workspace 为根,防 ../ 穿越;绝对路径限 workspace 内)
		abs := path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(p.root, abs)
		}
		clean := filepath.Clean(abs)
		root := filepath.Clean(p.root)
		if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return fmt.Errorf("sandbox: workspace-write 拒绝写 workspace 之外: %s", path)
		}
		return nil
	}
}

// CheckTool pre-execute 策略(按工具名,不解析参数——命令型工具一律按模式处理)。
func (p *Policy) CheckTool(name string) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	switch p.mode {
	case sdk.SandboxReadOnly:
		// read-only 禁用解释器/执行器:无法从参数判定是否只读的命令类工具直接 veto
		if isExecutor(name) {
			return fmt.Errorf("sandbox: read-only 拒绝执行器类工具 %q", name)
		}
		return nil
	case sdk.SandboxFullAccess:
		return nil
	default: // workspace-write
		// 写类工具由 ValidatePath 校验路径;命令类工具放行(默认档)
		return nil
	}
}

// isExecutor 判定命令/执行器类工具(未来扩展:run_code/lisp 等)。
func isExecutor(name string) bool {
	switch name {
	case "shell", "run_code", "lisp_eval", "bash":
		return true
	}
	return false
}

// workspaceRoot workspace 根:启动 cwd(后续支持 workspace_root 配置覆盖)。
func workspaceRoot() string {
	wd, _ := os.Getwd()
	return wd
}

// Default 供测试直接构造(workspace-write 默认档)。
func Default(root string) *Policy {
	p := &Policy{root: root}
	p.mode = sdk.SandboxWorkspace
	return p
}
