// 沙箱支路(原 policy-sandbox 逻辑原样迁移 + 有效档概念):三档路径/工具校验。
package policyguard

import (
	"encoding/json"
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

// EffectiveMode 档位联动后的有效档(实现 sdk.EffectiveSandbox;工具侧与状态展示对齐用)。
func (p *SandboxPolicy) EffectiveMode() sdk.SandboxMode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.effectiveMode()
}

// ValidatePath 写路径校验(read-only 拒绝一切;workspace-write 限制在 root 内,防 ../ 与 symlink 穿越;凭据类一律拒)。
// 与工具侧重复实现不同,此处是**唯一**裁决点(工具插件与宿主 pre-execute 均调它)。
func (p *SandboxPolicy) ValidatePath(path string) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	mode, root := p.effectiveMode(), p.root
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = filepath.Clean(abs)
	if err := denyPath(abs); err != nil {
		return err
	}
	switch mode {
	case sdk.SandboxReadOnly:
		return fmt.Errorf("sandbox: read-only 拒绝任何写操作")
	case sdk.SandboxFullAccess:
		return nil
	default: // workspace-write(realpath 归一后限 workspace 内)
		if !pathWithin(root, abs) {
			return fmt.Errorf("sandbox: workspace-write 拒绝写 workspace 之外: %s", path)
		}
		return nil
	}
}

// ValidateRead 读路径校验(实现 sdk.ReadValidator):
//   - full-access:放行;
//   - read-only / workspace-write:限 workspace 与 $GAH_HOME(附件/文档/缓存)内;
//   - 凭据类路径任何档位均拒(防 API key / 私钥进入模型上下文)。
func (p *SandboxPolicy) ValidateRead(path string) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	mode, root := p.effectiveMode(), p.root
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = filepath.Clean(abs)
	if err := denyPath(abs); err != nil {
		return err
	}
	if mode == sdk.SandboxFullAccess {
		return nil
	}
	if pathWithin(root, abs) {
		return nil
	}
	if h := sandboxGahHome(); h != "" && pathWithin(h, abs) {
		return nil
	}
	return fmt.Errorf("sandbox: 拒绝读 workspace 与数据根之外的路径: %s", path)
}

// CheckPathArgs 宿主侧路径裁决(P0 修复):默认发行态下 file_* 工具由外部插件进程提供
// (tool-files 未装配沙箱 → sb=nil),沙箱对其完全失效;此处按工具名+参数在 pre-execute
// 统一裁决,与具体实现无关(外部插件零改动)。
func (p *SandboxPolicy) CheckPathArgs(name, rawArgs string) error {
	kind, ok := fileToolKind(name)
	if !ok {
		return nil
	}
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
		return fmt.Errorf("sandbox: %s 参数无法解析出路径: %w", name, err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return fmt.Errorf("sandbox: %s 缺少 path 参数", name)
	}
	if kind == "write" {
		return p.ValidatePath(a.Path)
	}
	return p.ValidateRead(a.Path)
}

// fileToolKind 需宿主路径裁决的工具(name → read|write)。
// 含内置 tool-files 四件套与常见外部同名工具;其余工具不经此路径。
func fileToolKind(name string) (string, bool) {
	switch name {
	case "file_read", "file_list", "file_stat", "read_file":
		return "read", true
	case "file_write", "file_append", "file_edit", "file_delete", "write_file", "edit_file":
		return "write", true
	}
	return "", false
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
