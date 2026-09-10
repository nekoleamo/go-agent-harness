// 受控出站面(G-E5-3 IM-1a):ctx.imControl 的实现——把「已授权目标」与「按目标投递文本」
// 暴露给宿主工具(im_send/im_status),同时把 D1 安全口径钉在运行时层:
//
//	① 仅已授权目标:未授权/空目标显式报错,不隐式回落到「最后一个说话的人」(LastRoute);
//	② 投递走通道既有 SendText(预算层/主动配额/delivery ledger 全复用,无旁路);
//	③ 只读状态不含凭证;群投递用 Route.Group 显式标记(通道不支持则通道自行报错)。
package im

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Status 出站控制面只读状态(连接相位经 ctx.imChannels 现取,未装配则不谎报连通)。
func (b *Bridge) Status() sdk.IMControlStatus {
	b.mu.Lock()
	busy := b.busy
	b.mu.Unlock()
	st := sdk.IMControlStatus{
		Channel:    b.tr.Name(),
		Busy:       busy,
		Authorized: len(b.acc.List()),
		Groups:     len(b.acc.Groups()),
		Artifacts:  b.artifactCount(),
		Targets:    b.Targets(),
	}
	if cs := b.imConnect(); cs != nil {
		if phase := cs.ConnectStatus().Phase; phase != "" {
			st.Phase = phase
			st.Connected = phase == sdk.IMPhaseDone
		}
	}
	if b.llm != nil {
		st.Model = b.llm.Model()
	}
	if b.cwd != nil {
		st.Session = b.cwd.CurrentSession()
	}
	return st
}

// Targets 可投递目标:已授权用户 ∪ 已授权群(用户在前,各自按 key 稳定排序)。
func (b *Bridge) Targets() []sdk.IMSendTarget {
	channel := b.tr.Name()
	users := make([]string, 0, len(b.acc.List()))
	for _, key := range b.acc.List() {
		if id := b.stripChan(key); id != "" {
			users = append(users, id)
		}
	}
	sort.Strings(users)
	groups := make([]string, 0, len(b.acc.Groups()))
	for _, key := range b.acc.Groups() {
		if id := b.stripChan(key); id != "" {
			groups = append(groups, id)
		}
	}
	sort.Strings(groups)
	out := make([]sdk.IMSendTarget, 0, len(users)+len(groups))
	for _, u := range users {
		out = append(out, sdk.IMSendTarget{
			Key: u, Channel: channel, Label: "用户 " + u, UserID: u,
		})
	}
	for _, g := range groups {
		out = append(out, sdk.IMSendTarget{
			Key: g, Channel: channel, Label: "群 " + g, Group: true, ChatID: g,
		})
	}
	return out
}

// SendText 向已授权目标投递文本。
// target 为裸 openid(容忍 channel\x00id 写法,内部归一);未授权/空文本/通道未连接 → 显式错误。
func (b *Bridge) SendText(ctx context.Context, target, text string) error {
	id := strings.TrimSpace(target)
	if i := strings.Index(id, "\x00"); i >= 0 {
		id = id[i+1:]
	}
	if id == "" {
		return fmt.Errorf("im: 目标不能为空(用 im_status 查看可投目标)")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("im: 文本不能为空")
	}
	route, ok := b.routeForTarget(id)
	if !ok {
		return fmt.Errorf("im: 目标未授权或不存在: %s(先用 /im pair 或 /im allowg 授权)", id)
	}
	if err := b.sendText(ctx, route, text); err != nil {
		return fmt.Errorf("im: 投递失败(%s): %w", id, err)
	}
	return nil
}

// routeForTarget 已授权目标 → Route(未授权返回 ok=false)。群目标用 Group 显式标记。
func (b *Bridge) routeForTarget(id string) (Route, bool) {
	channel := b.tr.Name()
	if b.acc.Allowed(channel + "\x00" + id) {
		return Route{Channel: channel, UserID: id, ChatID: id}, true
	}
	for _, key := range b.acc.Groups() {
		if b.stripChan(key) == id {
			return Route{Channel: channel, Group: true, ChatID: id, UserID: id}, true
		}
	}
	return Route{}, false
}
