// Package policyapproval 提供 policy-approval 插件:危险操作检测 + 用户确认。
// 审批档位三档(对齐 policy-sandbox 三档先例,Provide ctx.approval 供运行期切换):
//   open 开放:危险操作直接放行,不弹确认(信任模型/无人值守)
//   smart 智能(默认):命中危险模式经 ctx.confirm 请求确认;无确认服务 → 拒绝(安全默认)
//   strict 严格:危险操作直接拒绝,不弹窗(最高防线)
package policyapproval

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
	{"递归删除", regexp.MustCompile(`\brm\s+-rf\b|\brm\s+--recursive\b|rm\s+-[a-zA-Z]*r\b`)},
	{"强制推送", regexp.MustCompile(`\bgit\s+push.*\s-f\b|\bgit\s+push.*--force\b`)},
	{"磁盘擦写", regexp.MustCompile(`\b(dd|mkfs|fdisk|parted)\b`)},
	{"权限后门", regexp.MustCompile(`\bchmod\s+777\b`)},
	{"特权操作", regexp.MustCompile(`\bsudo\b`)},
}

const confirmTimeout = 2 * time.Minute

// Plugin 实现 policy-approval。
type Plugin struct{}

func (p *Plugin) Name() string { return "policy-approval" }

// Start 挂载 pre-execute 拦截(确认服务缺失时不报错,策略按无通道拒绝)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var confirm sdk.ConfirmService
	_ = c.Inject("ctx.confirm", &confirm)

	pol := &Policy{mode: sdk.ApprovalSmart}
	if m != nil && m.Data != nil {
		if md, ok := m.Data["mode"].(string); ok && md != "" {
			pol.mode = sdk.ApprovalMode(md)
		}
	}
	if err := c.Provide("ctx.approval", pol); err != nil {
		return nil, err
	}

	d := c.Subscribe("tools/pre-execute", func(ctx context.Context, ev *sdk.Event) error {
		call, ok := ev.Payload.(*sdk.ToolCallEvent)
		if !ok || call.Name != "shell" {
			return nil
		}
		pattern, hit := matchDangerous(call.Arguments)
		if !hit {
			return nil
		}
		return pol.check(ctx, confirm, pattern)
	})
	return d, nil
}

// Policy 实现 sdk.ApprovalService(带锁,运行期可切档)。
type Policy struct {
	mu   sync.RWMutex
	mode sdk.ApprovalMode
}

func (p *Policy) Mode() sdk.ApprovalMode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

func (p *Policy) SetMode(m sdk.ApprovalMode) {
	p.mu.Lock()
	p.mode = m
	p.mu.Unlock()
}

// check 按档处理命中危险操作:open 放行 / smart 弹确认(无通道拒绝) / strict 直接拒绝。
func (p *Policy) check(ctx context.Context, confirm sdk.ConfirmService, pattern string) error {
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
		ok2, err := confirm.Confirm(cl, fmt.Sprintf("确认执行危险命令 [%s]? y/n", pattern))
		if err != nil {
			return fmt.Errorf("approval: 确认失败(%s): %v", pattern, err)
		}
		if !ok2 {
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
