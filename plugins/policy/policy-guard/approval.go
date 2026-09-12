// 审批支路(原 policy-approval 逻辑原样迁移):危险操作检测 + 用户确认。
package policyguard

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
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

const confirmTimeout = 2 * time.Minute

// ApprovalPolicy 实现 sdk.ApprovalService(带锁,运行期可切档)。
// 除「危险命令模式」(shell 文本启发)外,还承载**工具级审批名单**(E-A):
// data.approval_tools 列出的工具每次调用都按同一三档语义裁决(默认空 = 行为零变化)。
type ApprovalPolicy struct {
	mu    sync.RWMutex
	mode  sdk.ApprovalMode
	tools map[string]bool // 需审批工具名(不可变集,Start 时定下)
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
		cl, cancel := context.WithTimeout(ctx, confirmTimeout)
		defer cancel()
		if confirm == nil {
			return fmt.Errorf("approval: 检测到需审批的%s,无确认通道,已拒绝", label)
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
