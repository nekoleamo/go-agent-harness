// 审批支路(原 policy-approval 逻辑原样迁移):危险操作检测 + 用户确认。
package policyguard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 危险命令模式(常见高危集合;可配置扩展)。
var dangerousPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	// 删除操作全形态(真机反馈:rmdir/rm -f/rm -v 先后漏网 → 统一 rm/rmdir/unlink 全匹配;
	// 文本级启发,命令文本含删除词的会保守触发确认,可拒绝)。
	{"删除操作", regexp.MustCompile(`\brm\b|\brmdir\b|\bunlink\b`)},
	// 删除增强:shred/truncate(覆写/清空)、find -delete(批量删)。
	{"删除增强(shred/truncate/find -delete)", regexp.MustCompile(`\bshred\b|\btruncate\b|\bfind\b[^\n]*-delete\b`)},
	// 强制推送(含 --force-with-lease 同属强推变体;`git -C <repo> push -f`、`env git push --force`
	// 等带前置参数的形态也命中——不再要求 `git push` 紧邻)。
	{"强制推送", regexp.MustCompile(`\bgit\b[^\n]*\bpush\b[^\n]*(--force-with-lease|--force|-f\b)`)},
	// git 破坏性操作:reset --hard(丢工作区)、clean -f(删未跟踪)。
	{"git 破坏性操作(reset --hard/clean -f)", regexp.MustCompile(`\bgit\s+reset\s+--hard\b|\bgit\s+clean\s+-[a-zA-Z]*f[a-zA-Z]*\b`)},
	{"磁盘擦写", regexp.MustCompile(`\b(dd|mkfs|fdisk|parted)\b`)},
	// 权限后门:chmod 任何人可写(777/0777/4777)或 a+rwx/ugo+rwx、setuid/setgid(+s);
	// 刻意不命中 `chmod 644` / `chmod +x`(常规操作)。
	{"权限后门(chmod 777)", regexp.MustCompile(`\bchmod\b[^\n]*(\b777\b|\b0777\b|\b[0-7][0-7][0-7]7\b|a\+rwx|ugo\+rwx|\+s\b)`)},
	// 解释器内删除(非 rm 词形的删除路径)。
	{"解释器删除(脚本内删除)", regexp.MustCompile(`os\.remove\b|os\.unlink\b|shutil\.rmtree\b|\brmtree\b|\.unlink\(\)|\bRemove-Item\b|\bdel\s+/[fqs]`)},
	// 下载/编码内容直接管道进 shell(curl|sh、base64 -d | sh 等)。
	{"管道执行(下载/编码内容进 shell)", regexp.MustCompile(`(\bcurl\b|\bwget\b|base64\s+(-d|--decode))[^\n]*\|\s*(sudo\s+)?(sh|bash|zsh|dash)\b`)},
	// 覆写系统文件/块设备。
	{"覆写系统文件或设备", regexp.MustCompile(`>\s*/dev/(sd|disk|nvme)|>\s*/etc/`)},
	// 持久化后门(计划任务/服务注册/开机自启)。
	{"持久化后门(cron/服务/自启)", regexp.MustCompile(`\bcrontab\b|/etc/cron|\bsystemctl\s+(enable|mask)\b|\bschtasks\b|LaunchAgents`)},
	// 特权操作。
	{"特权操作(sudo/pkexec)", regexp.MustCompile(`\bsudo\b|\bpkexec\b`)},
}

// confirmTimeoutDefault 审批等待上限默认值:**0 = 不限时**。
// 为何改默认:原先硬编 2 分钟,人一离开(倒杯水/开会)回合就按「安全默认拒绝」往下走了,
// 而弹层还挂在界面上 —— 用户回来后既不知道已经拒了、也已经晚了(真机反馈:
// 「超时系统默认失败,继续进行」「应等到操作结果再继续,无结果不继续后续操作」)。
// 现在默认一直等到用户答复;终止只剩两条路:用户应答、或 ctx 被取消(界面按停止/Esc,
// 回合被取消 —— 取消本身就不会继续后续操作)。
// 需要「无人守候也不卡住」的场景用 data.confirm_timeout_sec 显式给一个 >0 的秒数。
const confirmTimeoutDefault time.Duration = 0

// ApprovalPolicy 实现 sdk.ApprovalService(带锁,运行期可切档)。
// 除「危险命令模式」(shell 文本启发)外,还承载**工具级审批名单**(E-A):
// data.approval_tools 列出的工具每次调用都按同一三档语义裁决(默认空 = 行为零变化)。
type ApprovalPolicy struct {
	mu    sync.RWMutex
	mode  sdk.ApprovalMode
	tools map[string]bool // 需审批工具名(不可变集,Start 时定下)
	// confirmTimeout 等待用户答复的上限(0 = 不限,见 confirmTimeoutDefault)。
	// 不可变集:Start 时从 data.confirm_timeout_sec 定下。
	confirmTimeout time.Duration
}

func (p *ApprovalPolicy) Mode() sdk.ApprovalMode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

func (p *ApprovalPolicy) SetMode(m sdk.ApprovalMode) {
	p.mu.Lock()
	p.mode = m
	p.mu.Unlock()
}

// check 按档处理命中危险操作:open 放行 / smart 弹确认(无通道拒绝) / strict 直接拒绝。
// command 为解出的真实命令文本(入确认弹层,避免"看不到命令就批准"的盲批)。
func (p *ApprovalPolicy) check(ctx context.Context, confirm sdk.ConfirmService, pattern, command string) error {
	label := fmt.Sprintf("危险命令 [%s]", pattern)
	if pv := argPreview(command, commandPreviewRunes); pv != "" {
		label += ": " + pv
	}
	return p.decide(ctx, confirm, label)
}

// commandPreviewRunes 确认弹层里命令文本的字符上限(长命令截断,保弹层可读)。
const commandPreviewRunes = 200

// shellCommand 从 shell 工具参数 JSON 解出真实命令文本再交给模式匹配:
// 直接对原始 JSON 匹配会被转义绕过(`\u0072m -rf /` 文本里看不到 rm,
// 执行侧解码后却是 rm)。解析失败时回落原文(宁可保守匹配,也不放过)。
func shellCommand(raw string) string {
	var a struct {
		Command string `json:"command"`
		Cmd     string `json:"cmd"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err == nil {
		if s := strings.TrimSpace(a.Command); s != "" {
			return s
		}
		if s := strings.TrimSpace(a.Cmd); s != "" {
			return s
		}
	}
	return raw
}

// checkTool 工具级审批(E-A):对 data.approval_tools 列出的工具每次调用裁决。
// subject = 工具名 + 参数摘要(有界),便于用户在确认里看清“要做什么”。
func (p *ApprovalPolicy) checkTool(ctx context.Context, confirm sdk.ConfirmService, tool, args string) error {
	subject := tool
	if pv := argPreview(args, toolArgPreviewRunes); pv != "" {
		subject += " " + pv
	}
	return p.decide(ctx, confirm, fmt.Sprintf("工具调用 [%s]", subject))
}

// RequiresToolApproval 该工具是否在需审批名单内(默认名单为空 → 恒 false)。
func (p *ApprovalPolicy) RequiresToolApproval(name string) bool {
	if name == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.tools[name]
}

// ApprovalTools 需审批工具名单(稳定顺序,诊断/测试用)。
func (p *ApprovalPolicy) ApprovalTools() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.tools))
	for name := range p.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// decide 三档裁决核心(命令支路与工具支路共用;label 描述待审批对象)。
func (p *ApprovalPolicy) decide(ctx context.Context, confirm sdk.ConfirmService, label string) error {
	// 无人值守(定时任务触发,NOND-W4):**没有在场的人**回答确认弹窗 ——
	// 一律拒绝,连 open 档也不放行(open 的语义是「你在场时不用问」,
	// 不是「没人问就等于同意」)。
	if sdk.UnattendedOf(ctx) {
		return fmt.Errorf("approval: 无人值守运行拒绝需审批的%s(定时任务没有确认通道;需人工确认的动作请手动执行)", label)
	}
	switch p.Mode() {
	case sdk.ApprovalOpen:
		return nil // 开放档:直接放行
	case sdk.ApprovalStrict:
		return fmt.Errorf("approval: 严格档拒绝需审批的%s", label)
	default: // smart(默认,现状行为)
		if confirm == nil {
			return fmt.Errorf("approval: 检测到需审批的%s,无确认通道,已拒绝", label)
		}
		cl := ctx
		if p.confirmTimeout > 0 {
			var cancel context.CancelFunc
			cl, cancel = context.WithTimeout(ctx, p.confirmTimeout)
			defer cancel()
		}
		ok, err := confirm.Confirm(cl, fmt.Sprintf("确认执行%s? y/n", label))
		if err != nil {
			return fmt.Errorf("approval: 确认失败(%s): %v", label, err)
		}
		if !ok {
			return fmt.Errorf("approval: 用户拒绝了%s", label)
		}
		return nil
	}
}

// parseConfirmTimeout 解析 data.confirm_timeout_sec(秒;<=0/缺项 = 不限时)。
// 宽容取值:yaml 解出来可能是 int / int64 / float64,也可能是字符串数字。
func parseConfirmTimeout(v any) time.Duration {
	switch t := v.(type) {
	case int:
		return time.Duration(t) * time.Second
	case int64:
		return time.Duration(t) * time.Second
	case float64:
		return time.Duration(t * float64(time.Second))
	case string:
		if n, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return time.Duration(n * float64(time.Second))
		}
	}
	return 0
}

// toolArgPreviewRunes 工具参数摘要的字符上限(确认弹层文本需短)。
const toolArgPreviewRunes = 120

// argPreview 参数摘要:压空字符、去首尾、限长(超限截断带省略号)。
func argPreview(args string, max int) string {
	s := strings.Join(strings.Fields(args), " ")
	if s == "" {
		return ""
	}
	r := []rune(s)
	if max > 0 && len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// parseApprovalTools 解析 data.approval_tools(容忍 []any / []string / 逗号或空白分隔字符串;空项忽略)。
func parseApprovalTools(v any) map[string]bool {
	out := map[string]bool{}
	add := func(s string) {
		for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
			if f != "" {
				out[f] = true
			}
		}
	}
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	case []string:
		for _, s := range t {
			add(s)
		}
	case string:
		add(t)
	}
	return out
}

// matchDangerous 返回命中的危险模式名。
func matchDangerous(args string) (string, bool) {
	for _, d := range dangerousPatterns {
		if d.re.MatchString(args) {
			return d.name, true
		}
	}
	return "", false
}

// ---------- 写目标派生(B2,2026-09-27 安全审计观察项) ----------
//
// 危险模式表是**枚举**正则:新增一种写法就多一个洞。实测漏网形态(均已核):
// `echo x >> /etc/hosts`(原 `>\s*/etc/` 不匹配双箭头)、`> ~/.ssh/authorized_keys`
// (持久化后门,原表只认字面 `LaunchAgents`)、`mv x /etc/y` / `ln -s x /etc/y`、`> ~/.zshrc`。
//
// 因此危险判据再加一条**派生**路:只看命令的**写目标落在哪** —— 而写目标表已在
// `shellpaths.go` 里(与路径裁决同一份解析,不重复枚举)。两者互补:枚举给“人读得懂的罪名”,
// 派生兜住没枚举到的写法。

// protectedWriteDirs 审批层的“受保护目录”(写它们 = 改系统;与枚举表互补,按**落点**判)。
//
// 刻意**不收** `/tmp`、`/var`、`/private/var`(macOS TMPDIR)、`$HOME` 下普通路径:
// 那些是常规工作落点,收了就是每条命令都弹窗 —— 噪音会把确认框训练成“闭眼点同意”。
//
// Windows 侧(2026-09-27 跨平台复核补):POSIX 那批字面量在 Windows 上根本不存在 ⇒
// 写 `C:\Windows\System32\drivers\etc\hosts` 既不命中枚举也无派生兜底。目录按环境变量现算
// (换成 D 盘 / 非默认 Program Files 也能对上);判定用 pathWithin(卷名与段名不区分大小写)。
func protectedWriteDirs() []string {
	dirs := []string{"/etc", "/usr", "/bin", "/sbin", "/boot", "/System", "/Library", "/opt", "/dev", "/root"}
	if runtime.GOOS != "windows" {
		return dirs
	}
	for _, env := range []string{"SystemRoot", "ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			dirs = append(dirs, filepath.Clean(v))
		}
	}
	return dirs
}

// protectedWriteFiles 家目录下的敏感文件(相对路径):写它们 = 劫持 shell/持久化。
var protectedWriteFiles = []string{
	".zshrc", ".zshenv", ".zprofile", ".bashrc", ".bash_profile", ".profile", ".gitconfig",
	".ssh/authorized_keys", ".ssh/config", ".ssh/known_hosts",
}

// derivedApprovalTarget 从命令的写目标派生审批项:命中的写目标 → (罪名, true)。
// 不可裁决的写目标(含变量/命令替换)**不跳过**:它们在 workspace-write/read-only 下已被路径层直接拒,
// 但 full-access 档路径检查整个短路 —— 那正是派生审批还有价值的地方(`echo x >> $HOME/.ssh/authorized_keys`)。
// 判定仍保守:只认能展开的写法(`$HOME/…`/`~`)与字面量凭据段,其余不命中。
func derivedApprovalTarget(cmd string) (string, bool) {
	for _, p := range shellCmdPaths(cmd) {
		if !p.Write {
			continue
		}
		if label, hit := protectedWriteTarget(p.Path); hit {
			return label, true
		}
	}
	return "", false
}

// protectedWriteTarget 写目标是否落在受保护位置(纯词法,不做 I/O)。
func protectedWriteTarget(raw string) (string, bool) {
	v := expandHomeVars(strings.TrimSpace(raw))
	if v == "" || v == "-" {
		return "", false
	}
	if hasShellExpansion(v) {
		// 目标不可知:只认字面量里出现的凭据路径(与读侧同一保守口径;不扫通配)
		for _, seg := range strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == '\t' }) {
			if sdk.LooksLikeCredentialPath(seg) {
				return "写凭据路径(变量/通配中的字面量段 " + seg + ")", true
			}
		}
		return "", false
	}
	p := v
	if strings.HasPrefix(p, "~") {
		exp, ok := expandTilde(p)
		if !ok {
			return "", false
		}
		p = exp
	}
	if !filepath.IsAbs(p) {
		return "", false // 相对路径落点由路径裁决管;审批层不猜(否则 workspace 内写会噪)
	}
	p = filepath.Clean(p)
	if sdk.LooksLikeCredentialPath(p) {
		return "写凭据路径 " + p, true
	}
	for _, d := range protectedWriteDirs() {
		if pathWithin(d, p) {
			return "写系统目录 " + d, true
		}
	}
	// 家目录取**候选并集**(HOME/USERPROFILE/UserHomeDir):审批是拒绝面,宁多问不漏判。
	for _, home := range sdk.UserHomes() {
		if !pathWithin(home, p) {
			continue
		}
		rel, err := filepath.Rel(home, p)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		for _, f := range protectedWriteFiles {
			if rel == f {
				return "写敏感配置 ~/" + f, true
			}
		}
	}
	return "", false
}

// expandHomeVars 把开头的 `$HOME`/`${HOME}`/`%USERPROFILE%` 换成真实家目录(写目标常这么写;
// 换了才能与“绝对路径”同一条判据判定)。其余变量不动 —— 落点不可知的一律交给后面的字面量扫描。
func expandHomeVars(s string) string {
	home := sdk.UserHome()
	if home == "" {
		return s
	}
	for _, pre := range []string{"${HOME}", "$HOME", "%USERPROFILE%"} {
		if strings.HasPrefix(s, pre) {
			rest := strings.TrimPrefix(strings.TrimPrefix(s, pre), string(filepath.Separator))
			if rest == "" {
				return filepath.Clean(home)
			}
			return filepath.Join(home, rest)
		}
	}
	return s
}
