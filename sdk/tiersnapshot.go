// 委派档位快照(pin):一次委派**发起那一刻**的有效审批/沙箱档,随 ctx 下传给子代理。
//
// 为什么需要(2026-10-24):后台子代理继承了父 ctx 的会话归属(第一百五十四批),
// 裁决链因此**每次现算** —— 这本身是对的(改设置下一毫秒即生效),但委派是一次跨回合的
// 授权:发起时是 strict,中途把会话放宽到 open,后台子代理就会按**新**档跑,而
// 「我授权这个子代理时是严格档」这条事实无处可查。
//
// 语义(与「子代理不得比父更宽」同一条纪律,只把比较基准从"父的会话"换成"发起那一刻"):
//   - 快照是**上限**:裁决取 min(现算档, 快照档),只能更严,永不放宽;
//   - 没有快照 ⇒ 行为逐字不变(旧宿主 / 测试替身 / 同步调用);
//   - 只影响经 bus 下传的裁决(policy-guard),不参与展示(状态栏仍显示现算档 ——
//     那才是"现在会怎样")。
package sdk

import "context"

// TierSnapshot 委派发起时刻的有效档位快照。空字段 = 该项未快照(不收紧)。
type TierSnapshot struct {
	Approval ApprovalMode
	Sandbox  SandboxMode
	// Source 快照来源标记(审计用;"role"/"approval"/"session"/"" = 与声明档一致)。
	// 只作记录,不参与裁决 —— 判定永远按档位本身,不按来源。
	Source string
}

// tierSnapshotKey 私有 key:防止包外塞入同名类型伪造快照。
type tierSnapshotKey struct{}

// WithTierSnapshot 把快照放进 ctx(委派入口调用)。
func WithTierSnapshot(ctx context.Context, s TierSnapshot) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, tierSnapshotKey{}, s)
}

// TierSnapshotFrom 取快照;ok=false 表示没有(旧路径:行为零变化)。
func TierSnapshotFrom(ctx context.Context) (TierSnapshot, bool) {
	if ctx == nil {
		return TierSnapshot{}, false
	}
	s, ok := ctx.Value(tierSnapshotKey{}).(TierSnapshot)
	return s, ok
}

// ApprovalAtMost 取更严者(快照为空 ⇒ 原样返回 cur、bySnapshot=false)。
// 复用 TightenApproval:它对非法档**故障闭合**(当最严),正好是快照该有的方向。
func (s TierSnapshot) ApprovalAtMost(cur ApprovalMode) (ApprovalMode, bool) {
	if s.Approval == "" {
		return cur, false
	}
	out, bySnap := TightenApproval(string(s.Approval), string(cur))
	return ApprovalMode(out), bySnap
}

// SandboxAtMost 取更严者(语义同 ApprovalAtMost)。
func (s TierSnapshot) SandboxAtMost(cur SandboxMode) (SandboxMode, bool) {
	if s.Sandbox == "" {
		return cur, false
	}
	out, bySnap := TightenSandbox(string(s.Sandbox), string(cur))
	return SandboxMode(out), bySnap
}

// SnapshotTiers 从服务取**该 ctx 所属会话**的有效档做成快照(委派入口用)。
//
// 取法:优先 *For(ctx)(会话级档 + 角色收紧 + 联动),没有则 EffectiveMode(),
// 再没有则声明档 —— 三层都与 policy-guard 的裁决链同源,不另写一份合成逻辑。
// 两个服务都拿不到 ⇒ ok=false(不注入空快照:空快照会让读端以为"有人在管")。
func SnapshotTiers(ctx context.Context, approval ApprovalService, sandbox Sandbox) (TierSnapshot, bool) {
	var s TierSnapshot
	got := false
	if approval != nil {
		m := approval.Mode()
		switch eff := approval.(type) {
		case EffectiveApprovalFor:
			m = eff.EffectiveModeFor(ctx)
		case EffectiveApproval:
			m = eff.EffectiveMode()
		}
		s.Approval = m
		got = true
	}
	if sandbox != nil {
		m := sandbox.Mode()
		switch eff := sandbox.(type) {
		case EffectiveSandboxFor:
			m = eff.EffectiveModeFor(ctx)
		case EffectiveSandbox:
			m = eff.EffectiveMode()
		}
		s.Sandbox = m
		got = true
	}
	return s, got
}
