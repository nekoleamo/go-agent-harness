// 档位联动(最薄一层):approval 为权威档位,驱动沙箱有效行为(sync=true 时)。
// 语义:
//
//	open   → 沙箱有效 full-access(执行器/写路径全放行,尊重"开放=别拦我")
//	strict → 沙箱有效 read-only(最高防线:危险命令拒 + 执行器拒 + 写路径拒)
//	smart  → 不覆盖,沙箱按自身档位(现状,零行为变化)
package policyguard

import "github.com/nekoleamo/go-agent-harness/sdk"

// effectiveMode 返回沙箱**有效**档:声明档 → 联动覆盖 → 角色收紧(顺序不可换)。
// 调用方必须已持 p.mu 读锁(与 ValidatePath / CheckTool 的锁约定一致)。
//
// 顺序为何是"联动在前、角色收紧在后":全局 open + sync=true 会把沙箱联动成 full-access;
// 若角色收紧算在联动之前,角色的 read-only 会被这次联动**覆盖掉** —— 角色档是下限,
// 任何路径都不得让它比全局更松(见 sdk.TightenSandbox)。
//
// 锁:`p.role()` 会去读 ctx.roles(host-roles 内存 map,无盘 I/O),嵌套的是两层读锁,
// 而 host-roles 侧从不反向获取沙箱锁 ⇒ 无环、不会死锁。
func (p *SandboxPolicy) effectiveMode() sdk.SandboxMode {
	return p.tightenRole(p.linkedMode())
}

// linkedMode 联动覆盖后的档(不含角色收紧)。
func (p *SandboxPolicy) linkedMode() sdk.SandboxMode {
	if p.sync && p.approval != nil {
		switch p.approval() {
		case sdk.ApprovalOpen:
			return sdk.SandboxFullAccess
		case sdk.ApprovalStrict:
			return sdk.SandboxReadOnly
		}
	}
	return p.mode
}

// tightenRole 角色收紧(只更严,绝不更松;未声明/无角色 ⇒ 原样返回)。
func (p *SandboxPolicy) tightenRole(m sdk.SandboxMode) sdk.SandboxMode {
	if p.role == nil {
		return m
	}
	if _, rs := p.role(); rs != "" {
		out, _ := sdk.TightenSandbox(rs, string(m))
		return sdk.SandboxMode(out)
	}
	return m
}
