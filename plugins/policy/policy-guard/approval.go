// 审批支路(原 policy-approval 逻辑原样迁移):危险操作检测 + 用户确认。
package policyguard

import (
	"context"
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
	// 删除操作全形态(IM 真机反馈:rmdir/rm -f/rm -v 先后漏网 → 统一 rm/rmdir/unlink 全匹配;
	// 文本级启发,命令文本含删除词的会保守触发确认,可拒绝)。
	{"删除操作", regexp.MustCompile(`\brm\b|\brmdir\b|\bunlink\b`)},
	// 删除增强:shred/truncate(覆写/清空)、find -delete(批量删)。
	{"删除增强(shred/truncate/find -delete)", regexp.MustCompile(`\bshred\b|\btruncate\b|\bfind\b[^\n]*-delete\b`)},
	// 强制推送(含 --force-with-lease 同属强推变体)。
	{"强制推送", regexp.MustCompile(`\bgit\s+push.*\s-f\b|\bgit\s+push.*--force\b|\bgit\s+push.*--force-with-lease\b`)},
	// git 破坏性操作:reset --hard(丢工作区)、clean -f(删未跟踪)。
	{"git 破坏性操作(reset --hard/clean -f)", regexp.MustCompile(`\bgit\s+reset\s+--hard\b|\bgit\s+clean\s+-[a-zA-Z]*f[a-zA-Z]*\b`)},
	{"磁盘擦写", regexp.MustCompile(`\b(dd|mkfs|fdisk|parted)\b`)},
	// 权限后门:chmod 含 777(覆盖 -R 777 与参数顺序变体)。
	{"权限后门(chmod 777)", regexp.MustCompile(`\bchmod\b[^\n]*\b777\b`)},
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
func (p *ApprovalPolicy) check(ctx context.Context, confirm sdk.ConfirmService, pattern string) error {
	return p.decide(ctx, confirm, fmt.Sprintf("危险命令 [%s]", pattern))
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

// toolArgPreviewRunes 工具参数摘要的字符上限(确认弹层/IM 文本均需短)。
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
