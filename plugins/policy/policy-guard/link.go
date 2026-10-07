// 档位联动(最薄一层):approval 为权威档位,驱动沙箱有效行为(sync=true 时)。
// 语义:
//
//	open   → 沙箱有效 full-access(执行器/写路径全放行,尊重"开放=别拦我")
//	strict → 沙箱有效 read-only(最高防线:危险命令拒 + 执行器拒 + 写路径拒)
//	smart  → 不覆盖,沙箱按自身档位(现状,零行为变化)
package policyguard

import (
	"context"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// effectiveMode 返回沙箱**有效**档:声明档 → 联动覆盖 → 角色收紧(顺序不可换)。
// 调用方必须已持 p.mu 读锁(与 ValidatePath / CheckTool 的锁约定一致)。
//
// 顺序为何是"联动在前、角色收紧在后":全局 open + sync=true 会把沙箱联动成 full-access;
// 若角色收紧算在联动之前,角色的 read-only 会被这次联动**覆盖掉** —— 角色档是下限,
// 任何路径都不得让它比全局更松(见 sdk.TightenSandbox)。
//
// 锁:`p.role(context.Background())` 会去读 ctx.roles(host-roles 内存 map,无盘 I/O),嵌套的是两层读锁,
// 而 host-roles 侧从不反向获取沙箱锁 ⇒ 无环、不会死锁。
func (p *SandboxPolicy) effectiveMode() sdk.SandboxMode {
	return p.effectiveModeWith(context.Background())
}

// effectiveModeWith 带会话上下文的有效档(第一百一十六批)。
//
// 顺序:**声明档 → 会话档 → 联动 → 角色收紧**。
// 会话档为什么在联动之前:它与角色一样是**下限**,不能被一次联动覆盖成更松 ——
// 否则用户给这个页签调成只读,全局 open 联动把它放宽,正好是用户最不想要的。
// "只更严"由 sdk.TightenSandbox/TightenApproval 保证(取更严者,永不放宽)。
func (p *SandboxPolicy) effectiveModeWith(ctx context.Context) sdk.SandboxMode {
	m := p.mode
	if ctx != nil {
		if v := sessionPref(ctx, func(x sdk.SessionPrefs) string { return x.Sandbox }); v != "" {
			// 会话级是**替换**声明档,不是"取更严" —— 用户给这个页签显式放宽到
			// full-access 是被拍板的语义(角色仍只能收紧)。用 TightenSandbox 那种
			// "取更严"会让"显式放宽"永远不生效,界面还显示自己设的值。
			m = sdk.SandboxMode(v)
		}
	}
	// 联动在会话档之后:审批 strict 是全局最高防线,它把沙箱压成只读时,
	// 会话级放宽不生效(这一条要在界面上说清楚)。
	return p.tightenRoleWith(p.linkedModeFrom(m), ctx)
}

// linkedMode 联动覆盖后的档(不含角色收紧)。
// linkedMode 联动覆盖后的档(不含角色收紧);无参版 = 从声明档起算(展示路径)。
func (p *SandboxPolicy) linkedMode() sdk.SandboxMode { return p.linkedModeFrom(p.mode) }

// linkedModeFrom 从给定档位做联动覆盖。
//
// 联动读的是**声明**审批档(全局权威档);会话级审批只参与"收紧",不参与联动方向 ——
// 联动是"审批开 → 沙箱全开"这条全局约定,让它按会话走会把"这个会话显式收紧"反过来放宽。
func (p *SandboxPolicy) linkedModeFrom(declared sdk.SandboxMode) sdk.SandboxMode {
	if p.sync && p.approval != nil {
		switch p.approval() {
		case sdk.ApprovalOpen:
			return sdk.SandboxFullAccess
		case sdk.ApprovalStrict:
			return sdk.SandboxReadOnly
		}
	}
	return declared
}

// tightenRoleWith 带会话上下文的角色收紧(该会话用的角色,而不是全局当前角色)。
func (p *SandboxPolicy) tightenRoleWith(m sdk.SandboxMode, ctx context.Context) sdk.SandboxMode {
	if p.role == nil {
		return m
	}
	if _, rs := p.role(ctx); rs != "" {
		out, _ := sdk.TightenSandbox(rs, string(m))
		return sdk.SandboxMode(out)
	}
	return m
}

// —— 会话级偏好的读数(第一百一十六批)——

// currentSessionPrefsSrc 由 Start 注入(ctx.cwdSessions 实现了 sdk.SessionPrefsSource 时)。
// 未注入 = 全部跟随全局(行为与改造前逐字一致)。
var currentSessionPrefsSrc sdk.SessionPrefsSource

// sessionPrefsReader 读该次调用所属会话的偏好。
//
// 为什么不是每次 Inject:裁决发生在**每次工具调用**的锁内;更重要的是本插件"每次裁决现算"
// 这条纪律不能破 —— 缓存会让改设置下一毫秒不生效(第八十七批 P1-3 正是这个病)。
func sessionPrefsReader(ctx context.Context) sdk.SessionPrefs {
	if ctx == nil {
		return sdk.SessionPrefs{}
	}
	if s := currentSessionPrefsSrc; s != nil { // ctx 里没有 session id 时(Background)读到的是主会话桶
		return s.SessionPrefsOf(sdk.SessionFromContext(ctx))
	}
	return sdk.SessionPrefs{}
}

// sessionPref 取偏好里的一项(空 = 该会话没设 = 跟随全局)。
func sessionPref(ctx context.Context, get func(sdk.SessionPrefs) string) string {
	if ctx == nil || get == nil {
		return ""
	}
	return get(sessionPrefsReader(ctx))
}
