// 档位联动(最薄一层):approval 为权威档位,驱动沙箱有效行为(sync=true 时)。
// 语义:
//
//	open   → 沙箱有效 full-access(执行器/写路径全放行,尊重"开放=别拦我")
//	strict → 沙箱有效 read-only(最高防线:危险命令拒 + 执行器拒 + 写路径拒)
//	smart  → 不覆盖,沙箱按自身档位(现状,零行为变化)
package policyguard

import "github.com/nekoleamo/go-agent-harness/sdk"

// effectiveMode 返回沙箱有效档(联动开启时按 approval 覆盖,否则原档位)。
// 调用方必须已持 p.mu 读锁(与 ValidatePath / CheckTool 的锁约定一致)。
func (p *SandboxPolicy) effectiveMode() sdk.SandboxMode {
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
