// notice.go:ctx.notices 面向用户的提示通道(NOND-N1 host-notices)。
//
// 定位(与其它通道的分工,勿混):
//   - **不是**模型可见的工具输出,也**不落**会话记录(不进 jsonl、不计 token、不是「模型可见即已记录」的对象);
//   - 是「需要人回来的时刻」的**主动提示**:无人值守任务失败、后台任务终止、回合报错等,
//     由发布方(presenter 之外的纯数据)经 ctx.notices 发出,各端自行决定呈现强度
//     (Web toast / TUI 状态行 / 桌面壳系统通知),不需要每加一个场景就在桌面壳再加一个轮询器。
//
// 生命周期:进程内环形缓冲(不落盘)。跨端重连/刷新后靠 List(sinceID) 回填;
// 端本身按 ID 去重(单调递增,进程内唯一)。
package sdk

import (
	"strings"
	"time"
	"unicode"
)

// NoticeLevel 提示级别(语义色;warn/error 才值得打断人:桌面壳通知/响铃只看这两级)。
type NoticeLevel string

const (
	NoticeInfo  NoticeLevel = "info"  // 信息(成功/状态变化)
	NoticeWarn  NoticeLevel = "warn"  // 需注意(降级、跳过、被迫放弃)
	NoticeError NoticeLevel = "error" // 出错(需人处理)
)

// EventNotice 提示事件(载荷 *Notice;订阅方 = Web 帧 / TUI 状态行 / 桌面壳通知)。
const EventNotice = "notice"

// NoticeDedupeWindow 提示去重窗口(同 Key 在此窗口内只保留第一条)。
// 动机:重试循环里的同一种错误、同一作业的重复终态不该把通知刷成一屏;
// 窗口取 60s —— 短于「人回来一趟」的量级,长于一次重试风暴。
const NoticeDedupeWindow = 60 * time.Second

// 提示字段上限(超出**裁剪并显式加省略号**,不静默丢数据):
// 提示是给人看的一行信息,不是日志搬运工 —— 细节请留 `$GAH_HOME/logs` 或会话记录。
const (
	NoticeTitleMax  = 80  // 标题(rune;折叠换行成单行)
	NoticeBodyMax   = 400 // 正文(rune)
	NoticeSourceMax = 64  // 来源(插件名/命令名)
)

// Notice 一条面向用户的提示。
type Notice struct {
	ID     uint64      `json:"id"`               // 单调递增(进程内唯一;端按此去重/续传)
	Level  NoticeLevel `json:"level"`            // info|warn|error(非法值归一为 info)
	Title  string      `json:"title"`            // 一句话(折叠单行;空则回落到正文首行)
	Body   string      `json:"body,omitempty"`   // 细节(错误原文/路径;可空)
	Source string      `json:"source,omitempty"` // 发布者(插件名/命令名;"unknown" = 未声明)
	TS     time.Time   `json:"ts"`

	// Key 去重键(可选;空 = 不去重)。同一 Key 在 NoticeDedupeWindow 内只保留第一条:
	// 重复触发同一件事(重试风暴/同一作业重复终态)不得把人刷死。
	// 内容不同的事件必须给不同 Key(否则会被误当重复丢掉)。
	Key string `json:"key,omitempty"`
}

// NoticePage 提示回填结果(List 的返回:回填窗口必须自我描述,不假装完整)。
type NoticePage struct {
	Items      []Notice `json:"items"`                // ID 升序(无提示时为空数组,不是 null)
	MaxID      uint64   `json:"max_id"`               // 当前已分配的最大 ID(客户端下次 sinceID)
	Gap        bool     `json:"gap,omitempty"`        // true = sinceID 之后的提示已被缓冲丢弃:本次回填不完整
	Suppressed uint64   `json:"suppressed,omitempty"` // 进程启动以来被去重丢弃的条数(可见可解释,不静默)
}

// NoticeService 提示通道(host-notices 提供 ctx.notices)。
// 未装配时调用方应跳过(可选注入),不阻断自身启动。
type NoticeService interface {
	// Publish 发布一条提示(ID/TS 由实现补齐;Level 非法归一 info;字段超长裁剪)。
	// 返回分配到的 ID;**0 = 该条被去重丢弃**(Key 在 NoticeDedupeWindow 内重复)。
	// 空标题空正文也会得到兜底文案(不因字段缺失而静默丢弃)。
	Publish(n Notice) uint64
	// List 取 ID 严格大于 sinceID 的提示(升序;sinceID=0 = 取当前缓冲全部)。
	List(sinceID uint64) NoticePage
}

// Normalize 归一一条提示(纯函数;Level/字段/空标题兜底),返回补齐后的副本。
// 用途:发布侧统一口径(host-notices 与测试共用),避免各发布点各写一份裁剪逻辑。
func (n Notice) Normalize() Notice {
	n.Level = n.Level.normalized()
	n.Source = clipLine(n.Source, NoticeSourceMax)
	if n.Source == "" {
		n.Source = "unknown" // 来源不可知也要可见,但不假装知道是谁发的
	}
	n.Title = clipLine(n.Title, NoticeTitleMax)
	n.Body = clipRunes(strings.TrimSpace(n.Body), NoticeBodyMax)
	if n.Title == "" {
		// 标题是列表/状态行的唯一可见字段:空标题不得让提示变成一行空白。
		n.Title = clipLine(firstNonEmptyLine(n.Body), NoticeTitleMax)
	}
	if n.Title == "" {
		n.Title = "有一条提示" // 兜底文案:宁可信息少,不可静默丢
	}
	return n
}

// normalized 级别归一(未知值 → info:不假装严重,也不因拼写错误而丢失)。
func (l NoticeLevel) normalized() NoticeLevel {
	switch l {
	case NoticeInfo, NoticeWarn, NoticeError:
		return l
	}
	return NoticeInfo
}

// clipLine 折叠成单行(换行/制表/连续空白 → 单空格)后按 rune 裁剪。
func clipLine(s string, max int) string {
	return clipRunes(strings.Join(strings.Fields(s), " "), max)
}

// clipRunes 按 rune 截断(rune 边界安全),超长补省略号。
func clipRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// firstNonEmptyLine 取首个非空白行(标题回落用)。
func firstNonEmptyLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimFunc(ln, unicode.IsSpace); t != "" {
			return t
		}
	}
	return ""
}
