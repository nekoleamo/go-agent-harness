// S-P1-2 会话窗口分页(Web 长会话):事件账本 → 尾部窗口 / 上滚分页,窗口边界**回合对齐**。
//
// 为什么需要:浏览器打开一个长会话时,原实现把**全量**事件重放一遍(万帧级 = 万次 JSON
// 解析 + 万条 DOM),首帧到可交互的延迟随会话长度线性增长。现在首连只回放尾部窗口,
// 更早历史由前端上滚时按 `before` 游标分页拉取(dsh follow 的 tail page + cursor 口径)。
//
// 为什么边界要回合对齐:前端把分页结果**拼在现有消息数组头部**(consume 是按事件序有状态的:
// 助手消息的工具行要靠后续 tool/result 事件回填)。若窗口切在一个回合中间,同一回合会被
// 两份状态各建一次 → 用户消息与助手消息在界面上重复。回合起点(`user/message`)对齐后,
// 每个回合完整属于某一页,拼接不会重复也不会丢。
package web

import (
	"github.com/nekoleamo/go-agent-harness/sdk"
)

const (
	// SessionTailEvents 首次连接回放的事件条数上限(窗口口径 = 事件,不是消息)。
	// 400 条 ≈ 二十到四十个回合,足够填满一屏并留出上滚余量;真实会话里绝大多数
	// 一次连接看不到更早的内容(hasMore=false),此窗口只是「不随会话长度劣化」的上界。
	SessionTailEvents = 400
	// sessionPageExtendMax 回合对齐时允许向前多取的事件条数上限。
	// 没有上限的话,一个「超长单回合」(几千条 tool 事件)会把窗口撑大到失去意义。
	sessionPageExtendMax = 200
	// SessionPageLimitMax 分页端点的 limit 上限(客户端可调小,不可调大)。
	SessionPageLimitMax = 2000
)

// pageEvents 从升序事件账本取一页(返回的切片与入参共享底层数组,调用方只读)。
//
//	before == 0 → 取尾部 limit 条(首连窗口)
//	before > 0  → 取 Seq < before 的尾部 limit 条(上滚分页)
//
// 取够 limit 后向前扩到**回合起点**(至多 sessionPageExtendMax 条),使每页从 user/message
// 开始;返回 hasMore 表示本页之前是否还有更早事件(前端据此决定「上滚加载」入口是否显示)。
func pageEvents(evs []sdk.SessionEvent, before uint64, limit int) (page []sdk.SessionEvent, hasMore bool) {
	if limit <= 0 {
		limit = SessionTailEvents
	}
	// 候选区间:Seq < before(账本升序,故 cut 之前全满足)
	cut := len(evs)
	if before > 0 {
		cut = 0
		for cut < len(evs) && evs[cut].Seq < before {
			cut++
		}
	}
	if cut <= limit {
		return evs[:cut], false
	}
	start := cut - limit
	// 回扩到回合起点:user/message 是回合的第一条**会进消息数组**的事件(turn/start 不产消息),
	// 只回扩到 user/message;若其前一条是 turn/start,一并纳入(保持「回合以 turn/start 起头」)。
	floor := start - sessionPageExtendMax
	if floor < 0 {
		floor = 0
	}
	for start > floor && evs[start].Kind != sdk.EventUserMessage {
		start--
	}
	if start > 0 && evs[start-1].Kind == sdk.EventTurnStart {
		start--
	}
	return evs[start:cut], start > 0
}

// sessionPage 分页端点的响应(前端上滚加载更早历史)。
type sessionPage struct {
	Events  []sdk.SessionEvent `json:"events"`
	From    uint64             `json:"from"`     // 本页最老事件 Seq(0 = 空页)
	To      uint64             `json:"to"`       // 本页最新事件 Seq
	Count   int                `json:"count"`    // 本页事件条数
	HasMore bool               `json:"has_more"` // 本页之前是否还有更早事件
}

// pageOf 组装分页响应(供 REST 端点与测试共用)。
func pageOf(evs []sdk.SessionEvent, before uint64, limit int) sessionPage {
	page, hasMore := pageEvents(evs, before, limit)
	// 空页归一为空数组(前端不遇 null)
	if page == nil {
		page = []sdk.SessionEvent{}
	}
	out := sessionPage{Events: page, Count: len(page), HasMore: hasMore}
	if len(page) > 0 {
		out.From = page[0].Seq
		out.To = page[len(page)-1].Seq
	}
	return out
}
