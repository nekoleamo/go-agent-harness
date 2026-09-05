// Package policyapproval 提供 policy-approval 插件:危险操作检测 + 用户确认。
// 监听 tools/pre-execute:命中危险模式经 ctx.confirm 请求确认;无确认服务 → 拒绝(安全默认)。
package policyapproval

import (
	"context"
	"fmt"
	"regexp"
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
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var confirm sdk.ConfirmService
	_ = c.Inject("ctx.confirm", &confirm)

	d := c.Subscribe("tools/pre-execute", func(ctx context.Context, ev *sdk.Event) error {
		call, ok := ev.Payload.(*sdk.ToolCallEvent)
		if !ok || call.Name != "shell" {
			return nil
		}
		pattern, hit := matchDangerous(call.Arguments)
		if !hit {
			return nil
		}
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
	})
	return d, nil
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
