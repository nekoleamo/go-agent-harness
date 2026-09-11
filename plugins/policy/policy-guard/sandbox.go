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

// CheckShellCommand shell 命令路径裁决(R10 ①,见 shellpaths.go):
//   - full-access:放行(与 ValidatePath 的档位语义一致);
//   - 写目标(重定向 / 写命令操作数)走 ValidatePath(档位 + 归属 + 凭据);
//     无法裁决的写形态(变量/通配前缀不可知/cd 出工作区后的相对路径)显式拒绝;
//   - 读目标只做凭据类判定,不做 workspace 归属限制(否则 shell 常规读被大面积误拦)。
//
// 危险模式审批不能替代本裁决:沙箱档位对 file_* 与 shell 一视同仁(审批"同意"不等于放开档位)。
func (p *SandboxPolicy) CheckShellCommand(cmd string) error {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	p.mu.RLock()
	mode, root := p.effectiveMode(), p.root
	p.mu.RUnlock()
	if mode == sdk.SandboxFullAccess {
		return nil
	}
	for _, pth := range shellCmdPathsRoot(cmd, 0, root) {
		if !pth.Write {
			if err := checkShellReadToken(root, pth.Path); err != nil {
				return err
			}
			continue
		}
		if pth.Unresolvable {
			return fmt.Errorf("sandbox: shell 命令含无法裁决的写目标 %q(含变量/命令替换,或切换出工作区后的相对路径);请改写为确定路径或切 /sandbox full", pth.Path)
		}
		if err := p.ValidatePath(pth.Path); err != nil {
			return fmt.Errorf("sandbox: shell 命令写目标被拒(%s): %w", pth.Path, err)
		}
	}
	return nil
}

// CheckPathArgs 宿主侧路径裁决(P0 修复):默认发行态下 file_* 工具由外部插件进程提供
// (tool-files 未装配沙箱 → sb=nil),沙箱对其完全失效;此处按工具名+参数在 pre-execute
// 统一裁决,与具体实现无关(外部插件零改动)。仅用内置工具名表(向后兼容入口)。
func (p *SandboxPolicy) CheckPathArgs(name, rawArgs string) error {
	return p.CheckToolCall(name, rawArgs, nil)
}

// CheckToolCall 能力驱动裁决(params = 工具自述的路径参数声明,见 sdk.PathParam):
// 声明非空用声明(支持自定义参数名/数组/可选参数),否则回退内置工具名表。
// 声明优先的意义:新插件工具名不受内置表覆盖(此前 save_file 之类名字下越界写不拦);
// 而内置名仍走表兜底,插件"声明为空"也无法借此绕过已知工具的裁决。
func (p *SandboxPolicy) CheckToolCall(name, rawArgs string, params []sdk.PathParam) error {
	if len(params) == 0 {
		params = builtinPathParams(name)
	}
	if len(params) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &m); err != nil {
		return fmt.Errorf("sandbox: %s 参数无法解析出路径: %w", name, err)
	}
	for _, pa := range params {
		raw, present := m[pa.Arg]
		if !present || raw == nil {
			if pa.Optional {
				continue
			}
			return fmt.Errorf("sandbox: %s 缺少 %s 参数", name, pa.Arg)
		}
		paths, err := pathValues(name, pa, raw)
		if err != nil {
			return err
		}
		for _, path := range paths {
			if strings.TrimSpace(path) == "" {
				if pa.Optional {
					continue
				}
				return fmt.Errorf("sandbox: %s 参数 %s 为空", name, pa.Arg)
			}
			if pa.Access == sdk.PathWrite {
				err = p.ValidatePath(path)
			} else {
				err = p.ValidateRead(path)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// pathValues 取参数值里的路径列表(字符串 / 字符串数组);类型不符显式报错。
func pathValues(name string, pa sdk.PathParam, raw any) ([]string, error) {
	switch v := raw.(type) {
	case string:
		return []string{v}, nil
	case []any:
		if !pa.Many {
			return nil, fmt.Errorf("sandbox: %s 参数 %s 应为字符串", name, pa.Arg)
		}
		out := make([]string, 0, len(v))
		for _, item := range v {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("sandbox: %s 参数 %s 含非字符串元素", name, pa.Arg)
			}
			out = append(out, str)
		}
		return out, nil
	case []string:
		return v, nil
	default:
		return nil, fmt.Errorf("sandbox: %s 参数 %s 应为字符串(或字符串数组)", name, pa.Arg)
	}
}

// builtinPathParams 内置工具名表 → 路径参数声明(未声明 PathParams 的工具走这条)。
func builtinPathParams(name string) []sdk.PathParam {
	kind, ok := fileToolKind(name)
	if !ok {
		return nil
	}
	access := sdk.PathRead
	if kind == "write" {
		access = sdk.PathWrite
	}
	return []sdk.PathParam{{Arg: "path", Access: access}}
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
