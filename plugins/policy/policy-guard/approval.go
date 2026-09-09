// 审批支路(原 policy-approval 逻辑原样迁移):危险操作检测 + 用户确认。
package policyguard

import (
	"context"
	"fmt"
	"regexp"
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
type ApprovalPolicy struct {
	mu   sync.RWMutex
	mode sdk.ApprovalMode
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
	switch p.Mode() {
	case sdk.ApprovalOpen:
		return nil // 开放档:直接放行
	case sdk.ApprovalStrict:
		return fmt.Errorf("approval: 严格档拒绝危险操作(%s)", pattern)
	default: // smart(默认,现状行为)
		cl, cancel := context.WithTimeout(ctx, confirmTimeout)
		defer cancel()
		if confirm == nil {
			return fmt.Errorf("approval: 检测到危险操作(%s),无确认通道,已拒绝", pattern)
		}
		ok, err := confirm.Confirm(cl, fmt.Sprintf("确认执行危险命令 [%s]? y/n", pattern))
		if err != nil {
			return fmt.Errorf("approval: 确认失败(%s): %v", pattern, err)
		}
		if !ok {
			return fmt.Errorf("approval: 用户拒绝危险操作(%s)", pattern)
		}
		return nil
	}
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
