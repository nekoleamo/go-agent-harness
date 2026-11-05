// extracthook.go:回合结束后通知记忆提取器(自动生成记忆的入口,P1)。
//
// 为什么由 agent-loop 主动调、而不是订阅 session 事件:会话事件通道按会话命名
// (`session/event/<id>`),而订阅 API 只支持精确名 —— 多会话并行时会漏;而 agent-loop
// 手里本来就有 `sess`,一次可选接口调用是最短路径(与 ContextualSystemPrompt 同款先例)。
//
// 三条纪律:
//  1. **非阻塞**:只调一次 ObserveTurn,投递到实现内部的队列;回合不等待模型请求;
//  2. **失败不影响回合**:缺服务、投递失败都只记日志,回合终态照旧;
//  3. **只给本回合的内容**:跨回合的原文不在这里传(提取器按限流决定要不要花这次钱)。
package hostagentloop

import (
	"context"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// digestClipRunes 传给提取器的单段长度上限(与 host-memory 的裁剪口径独立:
// 这里防的是"把整段日志塞进队列",那边防的是"塞进 prompt")。
const (
	digestUserRunes  = 2000
	digestReplyRunes = 600
)

// observeTurn 回合正常结束后把摘要交给记忆提取器。
func (l *Loop) observeTurn(sess sdk.SessionLog, sid, input string) {
	var ex sdk.MemoryExtractor
	if err := l.c.Inject("ctx.memoryExtract", &ex); err != nil || ex == nil {
		return // 未装配 ctx.memoryExtract(host-memory 未启用)= 没有这个能力,静默跳过
	}
	// 主会话没有文件 id(sessionIDOf 返回空)⇒ 用固定标识 "main"。
	// 为什么不能留空:记忆条目的**来源**是"按来源整段删"的唯一抓手,留空等于这批
	// 自动写的记忆没有回收入口。主会话本来就是一个确定的来源,用固定标识语义正确。
	sessionID := l.sessionIDOf(sid)
	if sessionID == "" {
		sessionID = "main"
	}
	d := sdk.TurnDigest{
		SessionID: sessionID,
		UserText:  clipDigest(input, digestUserRunes),
		ReplyText: lastAssistantText(sess),
	}
	if err := ex.ObserveTurn(context.Background(), d); err != nil {
		// 投递失败(队列满)只影响"这一回合没提取",不该让回合报错。
		l.c.Logger().Warn("agent-loop: 记忆提取投递失败(本回合不提取)", "err", err)
	}
}

// lastAssistantText 本会话投影里最后一条非空助手文本(提取器的"助手收尾"输入)。
func lastAssistantText(sess sdk.SessionLog) string {
	if sess == nil {
		return ""
	}
	msgs := sess.DeriveMessages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == sdk.RoleAssistant && strings.TrimSpace(msgs[i].Content) != "" {
			return clipDigest(msgs[i].Content, digestReplyRunes)
		}
	}
	return ""
}

func clipDigest(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
