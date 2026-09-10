// 群维度授权账本(G-E5-2):Web 面板/各端可列举「已授权 ∪ 最近活动」群并授权/撤销。
//
// 设计取舍:
//   - 授权名单是安全资产,**不自动过期**(避免静默失效导致意外拒绝);清理只能显式撤销。
//     长期无活动的授权群以 Stale 标记提示(面板可一键撤销);
//   - 群活动记录(未授权也记)有 TTL 与容量上限(见 noteGroup/pruneSeenLocked),窗口外的群
//     不再出现在「最近活动」列表中(拒绝面不靠列表,gate 仍是唯一裁决点);
//   - ChatID 对外一律为裸群 openid(渠道前缀仅在 access 键内部存在),展示/回传对称。
package im

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Groups 群维度授权全景(sdk.IMGroupAccessService):已授权 ∪ 最近活动。
// 排序:授权群在前(按最近活动倒序,无活动记录者最后),其余按最近活动倒序。
func (b *Bridge) Groups() []sdk.IMGroupEntry {
	now := time.Now()
	seen := map[string]time.Time{}
	for _, g := range b.seenGroups() {
		seen[g.chatID] = g.at
	}
	entries := make(map[string]sdk.IMGroupEntry, len(seen)+4)
	channel := b.tr.Name()
	for _, key := range b.acc.Groups() {
		id := b.stripChan(key)
		if id == "" {
			continue
		}
		entries[id] = sdk.IMGroupEntry{Channel: channel, ChatID: id, Authorized: true, Source: "authorized"}
	}
	for id, at := range seen {
		e := entries[id]
		if e.ChatID == "" {
			e = sdk.IMGroupEntry{Channel: channel, ChatID: id, Source: "seen"}
		} else {
			e.Source = "both"
		}
		e.LastSeen = at
		entries[id] = e
	}
	out := make([]sdk.IMGroupEntry, 0, len(entries))
	for _, e := range entries {
		// 长期无活动的授权群标 Stale(面板提示可撤销;不自动撤销)
		e.Stale = e.Authorized && (e.LastSeen.IsZero() || now.Sub(e.LastSeen) > staleGroupAfter)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Authorized != out[j].Authorized {
			return out[i].Authorized
		}
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].ChatID < out[j].ChatID
	})
	return out
}

// SetGroupAccess 授权(allow=true)/撤销(allow=false)一个群。
// chatID 为裸群 openid(允许带渠道前缀的写法,内部归一);撤销未知群显式报错(不静默)。
func (b *Bridge) SetGroupAccess(chatID string, allow bool) error {
	id := strings.TrimSpace(chatID)
	if i := strings.Index(id, "\x00"); i >= 0 { // 容忍 channel\x00chatID 写法
		id = id[i+1:]
	}
	if id == "" {
		return fmt.Errorf("im: 群 ChatID 不能为空")
	}
	key := b.chanKey(id)
	if allow {
		b.acc.AllowGroup(key)
		return nil
	}
	if !b.acc.RevokeGroup(key) {
		return fmt.Errorf("im: 该群未授权: %s", b.stripChan(key))
	}
	return nil
}
