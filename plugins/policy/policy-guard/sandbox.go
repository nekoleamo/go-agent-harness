// 沙箱支路(原 policy-sandbox 逻辑原样迁移 + 有效档概念):三档路径/工具校验。
package policyguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// SandboxPolicy 实现 sdk.Sandbox:三档(read-only / workspace-write / full-access)。
// sync=true 时校验行为按 effectiveMode(联动)执行;Mode()/SetMode() 保持档位原义
// (状态展示、prefs 持久化不受联动影响)。
type SandboxPolicy struct {
	mu       sync.RWMutex
	mode     sdk.SandboxMode
	root     string
	sync     bool                    // 档位联动开关(data.sync)
	approval func() sdk.ApprovalMode // 联动读数(guard 注入;不 import 审批支路,保解耦)
}

func (p *SandboxPolicy) Mode() sdk.SandboxMode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

func (p *SandboxPolicy) SetMode(m sdk.SandboxMode) {
	p.mu.Lock()
	p.mode = m
	p.mu.Unlock()
}

func (p *SandboxPolicy) SetRoot(dir string) {
	if dir == "" {
		return
	}
	p.mu.Lock()
	p.root = filepath.Clean(dir)
	p.mu.Unlock()
}

func (p *SandboxPolicy) Root() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.root
}

// effectiveMode 由 link.go 定义(调用方须持读锁)。

// ValidatePath 写路径校验(read-only 拒绝一切;workspace-write 限制在 root 内,防 ../ 穿越)。
func (p *SandboxPolicy) ValidatePath(path string) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	switch p.effectiveMode() {
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

// CheckTool pre-execute 工具级策略(按工具名,不解析参数——命令型工具一律按模式处理)。
func (p *SandboxPolicy) CheckTool(name string) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	switch p.effectiveMode() {
	case sdk.SandboxReadOnly:
		if isExecutor(name) {
			return fmt.Errorf("sandbox: read-only 拒绝执行器类工具 %q", name)
		}
		return nil
	case sdk.SandboxFullAccess:
		return nil
	default: // workspace-write
		return nil
	}
}

// isExecutor 判定命令/执行器类工具。
func isExecutor(name string) bool {
	switch name {
	case "shell", "run_code", "lisp_eval", "bash":
		return true
	}
	return false
}

// workspaceRoot 工作区根:启动 cwd(后续支持 workspace_root 配置覆盖)。
func workspaceRoot() string {
	wd, _ := os.Getwd()
	return wd
}

// DefaultSandbox 供测试直接构造(workspace-write 默认档;sync 关闭,联动不干扰单测)。
func DefaultSandbox(root string) *SandboxPolicy {
	return &SandboxPolicy{root: root, mode: sdk.SandboxWorkspace, sync: false}
}
