// 沙箱支路(原 policy-sandbox 逻辑原样迁移 + 有效档概念):三档路径/工具校验。
package policyguard

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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
	// role 当前角色的收紧档读数(guard 注入;空字符串 = 不收紧)。
	// **每次裁决现算**(guard 里的闭包现注 ctx.roles + 现取当前角色):缓存会让
	// "/plugins off|on host-roles 后策略冻结到重启"(第八十七批 P1-3 正是这个病)。
	role roleTiers
	// kernelScopeTool / kernelScopeShell 内核层当前**真正会施加**的写入面
	// (guard 注入;nil = 内核层不在场)。两者**必须分开**,因为两个执行面的内核 spec
	// 本来就不同 —— shell 侧把 TMPDIR 重定向进 jail(spec.RW 为空),外部插件侧才额外
	// 放行包管理器缓存与系统临时区。用一份清单覆盖两者,会让 `shell "echo x > /tmp/log"`
	// 从「路径层干净地拒」退化成「内核 EPERM 报错」—— 拒是拒了,但话说不清了。
	kernelScopeTool  func() []string
	kernelScopeShell func() []string
}

// kernelSurface 协作层面对的两条执行面。
type kernelSurface int

const (
	// surfaceTool 文件/工具类调用(默认形态下跑在**外部插件进程**里,spec.RW = 缓存+临时区)。
	surfaceTool kernelSurface = iota
	// surfaceShell shell/pty 命令(跑在 tool-shell 里,TMPDIR 已重定向进 jail,spec.RW 为空)。
	surfaceShell
)

// String 面名(日志/测试断言用)。
func (s kernelSurface) String() string {
	if s == surfaceShell {
		return "shell"
	}
	return "tool"
}

// SetKernelScopes 注入两个执行面的内核写入面读数(guard 在装配时给;测试可直接给)。
// 传 nil 的那一面视为「内核层不在场」,协作层对它保持窄口径。
func (p *SandboxPolicy) SetKernelScopes(tool, shell func() []string) {
	p.mu.Lock()
	p.kernelScopeTool, p.kernelScopeShell = tool, shell
	p.mu.Unlock()
}

// SetKernelScope 两个执行面都用同一份写入面(测试与「只有一个面」的简化场景)。
func (p *SandboxPolicy) SetKernelScope(fn func() []string) { p.SetKernelScopes(fn, fn) }

// kernelWritablePaths 当前可写的额外目录(内核层在场时 = 它那份清单,否则空)。
// 每次现算(档位/根/开关运行期可变):缓存会让「切了档没生效」,与第八十七批同款教训。
func (p *SandboxPolicy) kernelWritablePaths(s kernelSurface) []string {
	p.mu.RLock()
	fn := p.kernelScopeTool
	if s == surfaceShell {
		fn = p.kernelScopeShell
	}
	p.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// roleTiers 读当前角色的收紧档(approval, sandbox;"" = 不收紧)。
// 由 guard 注入,sandbox.go 与 approval.go 共用同一条读数(两个策略器必须同源,
// 否则会出现"审批按角色拒了、沙箱却按全局放行"的裂缝)。
type roleTiers func() (approval, sandbox string)

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

// SyncEnabled / SetSyncEnabled 档位联动开关(实现 sdk.SandboxSync;R10 ②-2)。
// 运行期切换不持久化 —— 持久化是调用方的事(命令/设置面板写 prefs),本类型只管语义。
func (p *SandboxPolicy) SyncEnabled() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sync
}

// SetSyncEnabled 切换联动开关(联动关掉后 effectiveMode() 直接回声明档)。
func (p *SandboxPolicy) SetSyncEnabled(on bool) {
	p.mu.Lock()
	p.sync = on
	p.mu.Unlock()
}

// effectiveMode 由 link.go 定义(调用方须持读锁)。

// EffectiveMode 档位联动 + 角色收紧后的有效档(实现 sdk.EffectiveSandbox;工具侧与状态展示对齐用)。
func (p *SandboxPolicy) EffectiveMode() sdk.SandboxMode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.effectiveMode()
}

// EffectiveFrom 有效档 != 声明档的**来源**(实现 sdk.EffectiveSource):
// 角色收紧 / 审批联动 / ""(一致)。三端展示据此说实话 —— 写死"联动所致"在角色收紧时是假话。
func (p *SandboxPolicy) EffectiveFrom() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	declared := p.mode
	linked := p.linkedMode() // 联动后、角色收紧前
	if p.role != nil {
		if _, rs := p.role(); rs != "" {
			if _, byRole := sdk.TightenSandbox(rs, string(linked)); byRole {
				return sdk.TierSourceRole
			}
		}
	}
	if linked != declared {
		return sdk.TierSourceApproval
	}
	return ""
}

// ValidatePath 写路径校验(read-only 拒绝一切;workspace-write 限制在 root 内
// **加上内核层放行的落点**(jail/临时区/包缓存,见 kernelWritablePaths),防 ../ 与
// symlink 穿越;凭据类一律拒)。
// 与工具侧重复实现不同,此处是**唯一**裁决点(工具插件与宿主 pre-execute 均调它)。
func (p *SandboxPolicy) ValidatePath(path string) error {
	return p.ValidatePathAt(p.Root(), path)
}

// ValidatePathAt 以显式 root 为写范围校验(S-P1-4 隔离运行:root = 本次调用工作根/受管 worktree)。
// root 空 → 退回自身 root(未隔离调用行为不变)。
func (p *SandboxPolicy) ValidatePathAt(root, path string) error {
	return p.validatePathAt(root, path, surfaceTool)
}

// validatePathAtAt 同一判定,显式指定执行面(shell 命令走 surfaceShell —— 它的内核 spec 与
// 工具面不同,见 kernelScopeTool 注释)。
func (p *SandboxPolicy) validatePathAt(root, path string, surface kernelSurface) error {
	// URL 当路径:模型会把网页地址交给写工具(含 shell 重定向),于是在 cwd 下长出
	// `https:/host/docs/…` 空目录树(2026-09-22 真机)。
	// **必须在拼 root 之前判原始入参** —— 相对形态经 filepath.Join 后 URL 前缀就没了(只剩 <root>/https:/…)。
	// 放在档位判定之前:full-access 同样拦 —— 这不是策略松紧,而是 URL 永远不是本地路径。
	if sdk.LooksLikeURLPath(path) {
		return fmt.Errorf("sandbox: 拒绝把 URL 当成文件路径: %s(抓网页请用 web 工具)", path)
	}
	p.mu.RLock()
	mode, own := p.effectiveMode(), p.root
	p.mu.RUnlock()
	if root == "" {
		root = own
	}
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
	default: // workspace-write(realpath 归一后限本次调用工作根内)
		if pathWithin(root, abs) {
			return nil
		}
		// 工作区根之外,先看**内核层放不放行**(jail/临时区/包缓存/插件自报数据目录)。
		// 内核层在场就以它为准 —— 边界由内核定义,协作层与之对齐(而不是比它更严地
		// 拒掉同一批落点,让模型在同一堵墙上反复换路径重试)。
		for _, w := range p.kernelWritablePaths(surface) {
			if pathWithin(w, abs) {
				return nil
			}
		}
		if root != own {
			// 隔离运行:明确说“本次工作根”而非“workspace”—— 否则子代理看到的消息会误导它去改主工作区
			return fmt.Errorf("sandbox: 隔离运行拒绝写本次工作根之外: %s(本次工作根 %s)", path, root)
		}
		return fmt.Errorf("sandbox: workspace-write 拒绝写 workspace 之外: %s", path)
	}
}

// ValidateRead 读路径校验(实现 sdk.ReadValidator):
//   - full-access:放行;
//   - read-only / workspace-write:限 workspace 与 $GAH_HOME(附件/文档/缓存)内;
//   - 凭据类路径任何档位均拒(防 API key / 私钥进入模型上下文)。
func (p *SandboxPolicy) ValidateRead(path string) error {
	return p.ValidateReadAt(p.Root(), path)
}

// ValidateReadAt 以显式 root 为**相对路径基准**校验读(S-P1-4)。
// 隔离运行只收窄**写**落点,不缩小**读**范围 —— 子代理在 worktree 内工作,但仍需读主工作区
// 里未跟踪的文件(生成物/本地配置),把它们一并拒死会让隔离在实践中不可用。
// 凭据类与 GAH_HOME 外部读限制不变。
func (p *SandboxPolicy) ValidateReadAt(root, path string) error {
	p.mu.RLock()
	mode, own := p.effectiveMode(), p.root
	p.mu.RUnlock()
	if root == "" {
		root = own
	}
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
	if own != "" && own != root && pathWithin(own, abs) {
		return nil // 隔离运行:主工作区文件仍可读(只限制写)
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
	return p.CheckShellCommandAt(p.Root(), cmd)
}

// CheckShellCommandAt 以显式 root 裁决 shell 命令写目标(S-P1-4 隔离运行:root = 本次工作根)。
// **相对写路径以 root 为基准解析**(与工具侧 cmd.Dir 一致 —— 两边不同基准 = “以为拦住了其实没拦”)。
func (p *SandboxPolicy) CheckShellCommandAt(root, cmd string) error {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	p.mu.RLock()
	mode, own := p.effectiveMode(), p.root
	p.mu.RUnlock()
	if root == "" {
		root = own
	}
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
			return fmt.Errorf("sandbox: shell 命令含无法裁决的写目标 %q(含变量/命令替换、切换出工作区后的相对路径,或 Windows/MSYS 根相对路径如 /c/…、/tmp/…);请改写为确定路径或切 /sandbox full", pth.Path)
		}
		if err := p.validatePathAt(root, pth.Path, surfaceShell); err != nil {
			return fmt.Errorf("sandbox: shell 命令写目标被拒(%s): %w", pth.Path, err)
		}
	}
	return nil
}

// CheckPathArgs 宿主侧路径裁决(P0 修复):默认发行态下 file_* 工具由外部插件进程提供
// (tool-files 未装配沙箱 → sb=nil),沙箱对其完全失效;此处按工具名+参数在 pre-execute
// 统一裁决,与具体实现无关(外部插件零改动)。
// 无工具定义(向后兼容入口)→ 内置工具名表 + 值级兜底(见 CheckToolCallAt)。
func (p *SandboxPolicy) CheckPathArgs(name, rawArgs string) error {
	return p.CheckToolCallAt(p.Root(), name, rawArgs, sdk.ToolDefinition{})
}

// CheckToolCall 能力驱动裁决(params = 工具自述的路径参数声明,见 sdk.PathParam):
// 声明非空用声明(支持自定义参数名/数组/可选参数),否则回退内置工具名表与推断/兜底。
// 声明优先的意义:新插件工具名不受内置表覆盖(此前 save_file 之类名字下越界写不拦);
// 而内置名仍走表兜底,插件“声明为空”也无法借此绕过已知工具的裁决。
func (p *SandboxPolicy) CheckToolCall(name, rawArgs string, params []sdk.PathParam) error {
	return p.CheckToolCallAt(p.Root(), name, rawArgs, sdk.ToolDefinition{PathParams: params})
}

// CheckToolCallAt 同 CheckToolCall,但以显式 root 为本次调用的写范围/相对路径基准
// (S-P1-4 隔离运行)。root 空 = 退回自身 root。
//
// 裁决依据按四级收敛(2026-09-27 安全审计 F2:此前“声明为空 + 工具名不在内置表”直接放行,
// 第三方插件工具与 MCP 工具因此完全不受路径沙箱约束):
//
//	① 工具自述声明 def.PathParams(声明优先);
//	② 内置工具名表 builtinPathParams(name);
//	③ 按定义推断 sdk.InferPathParams(def)(schema 参数名 + 工具名动词);
//	④ 值级兜底 sniffPathParams(参数值一眼是路径就按工具名的读写意图裁决)。
//
// def.PathParamsDeclared = true 时跳过 ③④(作者明确“本工具没有路径参数”)。
func (p *SandboxPolicy) CheckToolCallAt(root, name, rawArgs string, def sdk.ToolDefinition) error {
	if root == "" {
		root = p.Root()
	}
	params := def.PathParams
	if len(params) == 0 {
		params = builtinPathParams(name)
	}
	if len(params) == 0 {
		params = sdk.InferPathParams(def)
	}
	var m map[string]any
	sniffed := false
	switch {
	case len(params) > 0:
		if err := json.Unmarshal([]byte(rawArgs), &m); err != nil {
			return fmt.Errorf("sandbox: %s 参数无法解析出路径: %w", name, err)
		}
	case def.PathParamsDeclared:
		return nil // 作者显式声明:无路径参数(③④ 均跳过)
	default:
		// 值级兜底:解析不出参数对象 = 没有路径面可判,不因启发式把调用打成失败
		if json.Unmarshal([]byte(rawArgs), &m) != nil {
			return nil
		}
		params = sniffPathParams(name, m)
		if len(params) == 0 {
			return nil // 四级全空:确实没有路径面
		}
		sniffed = true
	}
	for _, pa := range params {
		paths, present, err := resolveParamPaths(name, pa, m)
		if err != nil {
			return err
		}
		if !present {
			if pa.Optional {
				continue
			}
			return fmt.Errorf("sandbox: %s 缺少 %s 参数", name, paramLabel(pa))
		}
		for _, path := range paths {
			if strings.TrimSpace(path) == "" {
				if pa.Optional {
					continue
				}
				return fmt.Errorf("sandbox: %s 参数 %s 为空", name, paramLabel(pa))
			}
			if pa.Access == sdk.PathWrite {
				err = p.ValidatePathAt(root, path)
			} else {
				err = p.ValidateReadAt(root, path)
			}
			if err != nil {
				switch {
				case len(pa.Nested) > 0:
					// 嵌套参数:点名实际取值路径(否则作者只看到“工作区外”,不知道该改哪个字段)
					return fmt.Errorf("sandbox: %s 参数 %s: %w", name, paramLabel(pa), err)
				case sniffed:
					// 兜底判定必须说清楚**为何**被拒 + 怎么解除:否则第三方插件作者只能看到一条
					// “工作区外”消息,无从知道宿主在用启发式看着他
					return fmt.Errorf("sandbox: 工具 %s 未声明路径参数,已按保守规则裁决参数 %q: %w"+
						"(插件作者请显式声明 PathParams,见 docs/PLUGIN_DEV.md §2.6;确无路径参数请设 PathParamsDeclared)",
						name, paramLabel(pa), err)
				}
				return err
			}
		}
	}
	return nil
}

// resolveParamPaths 取出本次要裁决的路径值(顶层字段 / 嵌套路径两种形态)。
//
// 返回 present=false 表示“路径面不存在”(嵌套取不到东西、或顶层参数缺失)→ 由调用方按 Optional 处理;
// 单条值不是字符串时**跳过**而不报错:嵌套形态下 items 可能混着非路径字段(见 InferPathParams 注释)。
func resolveParamPaths(name string, pa sdk.PathParam, m map[string]any) ([]string, bool, error) {
	if len(pa.Nested) > 0 {
		vals := sdk.LookupArgPath(m, pa.Nested)
		var out []string
		for _, v := range vals {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out, len(out) > 0, nil
	}
	raw, present := m[pa.Arg]
	if !present || raw == nil {
		return nil, false, nil
	}
	paths, err := pathValues(name, pa, raw)
	return paths, true, err
}

// paramLabel 声明的可读名(嵌套形态展示实际取值路径,便于作者定位)。
func paramLabel(pa sdk.PathParam) string {
	if len(pa.Nested) > 0 {
		return strings.Join(pa.Nested, ".")
	}
	return pa.Arg
}

// sniffSkipArgs 值级兜底**跳过**的参数名:按惯例承载“内容/指令/查询词”而非路径 ——
// 其中的绝对路径形态不是路径用法(`web_search{query:"/etc/hosts"}` 是搜索词)。
//
// **但凭据面不豁免**(A2,2026-09-27):`{url:"~/.ssh/id_rsa"}` 这类值即使长在内容参数名上
// 也照常裁决(见 sdk.LooksLikeCredentialPath)—— 搜索词是常态,拿凭据路径当内容参数的值不是。
//
// 代价(显式登记):把非凭据路径藏在 `input`/`query` 这类名字里的未声明工具仍会漏过 —— 但正常写法的
// 路径参数名(“path/file/dir/…”)**已被上一级参数名推断覆盖**,而误拒一条搜索词/命令的代价
// 更大且用户无从理解(错误文案只能告诉他“工作区外”)。
var sniffSkipArgs = map[string]bool{
	"command": true, "cmd": true, "script": true, "code": true, "input": true,
	"query": true, "q": true, "url": true, "uri": true, "pattern": true, "regex": true,
	"prompt": true, "text": true, "content": true, "body": true, "message": true,
	"description": true, "subject": true, "objective": true, "request": true, "task": true,
	"filter": true,
}

// sniffPathParams 值级兜底:未声明且推断不出时,扫**顶层**参数里“一眼是路径”的值。
//
// 只扫顶层(不递归对象) —— 递归会在 `params:{…}` 这类大 JSON 参数里误判;
// 数组只认**全字符串**项(含非字符串项时整条跳过,与 sdk.InferPathParams 对 array of object
// 的处理一致:避免因“含非字符串元素”把合法调用打成失败)。
// 读写意图取工具名动词(sdk.InferAccess),不明时按 write(更严)。
// 值级兜底递归上限(A8,2026-09-27 审计 A1 遗留项④)。
//
// 为何必须有界:未声明工具的参数是**任意外形**,而判据来自被约束方(插件给的 JSON)—— 递归
// 无界等于把裁决变成 CPU/内存放大器。超限就停,并且**只提醒一次**(slog):这不是错误,
// 是“未裁决面”的可见化(与 A3 的“能力缺失一律明示”同口径)。
const (
	sniffMaxDepth = 4
	sniffMaxKeys  = 64
	sniffMaxItems = 64
)

var sniffLimitOnce sync.Once

func sniffLimitNote(what string) {
	sniffLimitOnce.Do(func() {
		slog.Warn("policy-guard: 值级兜底命中结构上限,超出部分未参与路径裁决", "limit", what,
			"hint", "插件请显式声明 PathParams(见 docs/PLUGIN_DEV.md §2.6)")
	})
}

// sniffParam 拼一个值级兜底的路径参数(Nested 仅在确实下钻过时给出)。
func sniffParam(keyPath []string, access sdk.PathAccess, many bool) sdk.PathParam {
	pa := sdk.PathParam{Arg: keyPath[0], Access: access, Optional: true}
	if len(keyPath) > 1 {
		pa.Nested = keyPath
		return pa // 嵌套形态由 Nested 承载取值;Many 只用于顶层数组
	}
	pa.Many = many
	return pa
}

// sniffKey 去重/排序键(同一嵌套路径被多个数组元素命中时只留一条)。
func sniffKey(p sdk.PathParam) string { return p.Arg + "|" + strings.Join(p.Nested, ".") }

// sniffPathParams 值级兜底:参数值一眼是路径就按工具名的读写意图裁决(四级收敛的第④级)。
//
// A8 起**递归**到对象/数组内部(此前只看顶层字符串与字符串数组):嵌套参数里藏的越界路径
// (如 `{"options":{"files":[{"path":"/etc/hosts"}]}}`)同样要拦。两条反向豁免(执行器类工具、
// 内容/指令类参数名)在**每一层**同深生效;凭据路径例外照旧(内容名下的凭据路径仍裁)。
func sniffPathParams(name string, m map[string]any) []sdk.PathParam {
	if isExecutor(name) {
		return nil // 执行器类:命令/脚本文本不是路径(absolute 形态的命令是正常写法)
	}
	access := sdk.InferAccess(name)
	var out []sdk.PathParam
	seen := map[string]bool{}
	sniffWalk(m, nil, 1, access, &out, seen)
	// map 遍历无序 → 定序(错误文案要可断言)
	sort.Slice(out, func(i, j int) bool { return sniffKey(out[i]) < sniffKey(out[j]) })
	return out
}

// sniffWalk 递归收集路径值。keyPath 用 `*` 表示“穿过一层数组”(与 sdk.InferPathParams 同约定,
// sdk.LookupArgPath 按 `*` 展开数组):顶层数组仍是 Many,嵌套数组用 Nested+`*`。
func sniffWalk(m map[string]any, prefix []string, depth int, access sdk.PathAccess, out *[]sdk.PathParam, seen map[string]bool) {
	if depth > sniffMaxDepth {
		sniffLimitNote("depth")
		return
	}
	keys := 0
	for k, v := range m {
		if keys++; keys > sniffMaxKeys {
			sniffLimitNote("keys")
			return
		}
		skip := sniffSkipArgs[strings.ToLower(k)]
		keyPath := append(append([]string(nil), prefix...), k)
		add := func(p sdk.PathParam) {
			if key := sniffKey(p); !seen[key] {
				seen[key] = true
				*out = append(*out, p)
			}
		}
		switch t := v.(type) {
		case string:
			if sdk.LooksLikePathValue(t) && (!skip || sdk.LooksLikeCredentialPath(t)) {
				add(sniffParam(keyPath, access, false))
			}
		case map[string]any:
			sniffWalk(t, keyPath, depth+1, access, out, seen)
		case []any:
			if len(t) > sniffMaxItems {
				sniffLimitNote("items")
				t = t[:sniffMaxItems]
			}
			// 混合数组:字符串元素按“穿过一层数组”裁决,对象元素继续下钻
			strHit, hadObj := false, false
			for _, item := range t {
				switch it := item.(type) {
				case string:
					if sdk.LooksLikePathValue(it) && (!skip || sdk.LooksLikeCredentialPath(it)) {
						strHit = true
					}
				case map[string]any:
					hadObj = true
					sniffWalk(it, append(keyPath, "*"), depth+1, access, out, seen)
				}
			}
			if strHit {
				// 顶层数组仍给 Many(取值形态是字符串数组);嵌套数组走 Nested+`*`
				if len(keyPath) == 1 && !hadObj {
					add(sniffParam(keyPath, access, true))
				} else {
					add(sniffParam(append(keyPath, "*"), access, false))
				}
			}
		}
	}
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
//
// `powershell`(NOND-W1b,Windows 侧执行器)与 shell 同类,同样**不在这里**按路径参数
// 解析命令文本 —— 它走 `powershellCmdPaths` 那一支(见 CheckExecutorCommandAt),
// 否则值级兜底会把命令里的绝对路径当普通参数路径裁掉(read-only 档会误拒,
// 而更大的问题是**写**目标走不到 PowerShell 专用的扫描器)。
func isExecutor(name string) bool {
	switch name {
	case "shell", "powershell", "run_code", "lisp_eval", "bash":
		return true
	}
	return false
}

// CheckExecutorCommandAt 按工具名选对应的写目标扫描器裁决一次命令。
//
// 为什么在这里分派而不是让调用方各自判断:「哪个工具用哪套语法」是**策略层**的知识
// (漏一处 = 一个执行器没有写裁决),集中在一行 switch 里最容易在新增执行器时被看见。
func (p *SandboxPolicy) CheckExecutorCommandAt(root, name, cmd string) error {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	if name != "powershell" {
		return p.CheckShellCommandAt(root, cmd)
	}
	p.mu.RLock()
	mode := p.effectiveMode()
	p.mu.RUnlock()
	if mode == sdk.SandboxFullAccess {
		return nil
	}
	if root == "" {
		root = p.Root()
	}
	for _, pth := range powershellCmdPaths(cmd) {
		if !pth.Write {
			if err := checkShellReadToken(root, pth.Path); err != nil {
				return err
			}
			continue
		}
		if pth.Unresolvable {
			return fmt.Errorf("sandbox: powershell 命令含无法裁决的写目标 %q(含 $ 变量/子表达式/通配/调用表达式);"+
				"请改写为确定路径,或切 /sandbox full 后自行确认", pth.Path)
		}
		if err := p.validatePathAt(root, pth.Path, surfaceShell); err != nil {
			return fmt.Errorf("sandbox: powershell 命令写目标被拒(%s): %w", pth.Path, err)
		}
	}
	return nil
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
