// Package tooloffload 工具结果的**数据面/控制面分流**(第一百六十五批)。
//
// 借鉴 Octop 的 `octop_ui` 旁路设计(docs/octop-ui-payload-offload.md),但按 gah 的
// 形状重做:不新增中间件、不新增存储 —— 只在**结果过粗**时把原文搬到
// sdk.ToolResult.Data,把 Content 换成「头 + 省略说明 + 尾」。
//
// 为什么需要(实打实的三条):
//  1. 今天的工具结果**没有执行期上限**:`shell` 跑一次 `cat 大文件`,整份进模型上下文;
//     真要撑爆也只会在下一轮由 token-compress 被动折叠,代价是那轮请求本身。
//  2. 界面与模型抢同一份数据:Web 的「展开全文」读的就是 Content,于是"给模型瘦身"
//     和"给用户看全"是同一个开关 —— 只能二选一。
//  3. 历史投影的 clipOldToolResults 只裁**旧**结果(head/tail),最新一条永远全量 ——
//     恰恰是最容易撑爆的那一条不裁。
//
// 三条纪律(写进代码,免得后来改坏):
//   - **不静默**:所有被裁的地方都带可见说明(原字符数 + 省略量),模型知道"这里少了东西";
//   - **优先保结构**:能解析成 JSON 对象就按字段裁(模型仍能读到 count/status 等结论),
//     实在裁不进预算才退化成信封(仍是合法 JSON,不是半截文本);
//   - **只说真话**:原文一个字不改地进 Data;界面上看到的就是工具真正输出的。
package tooloffload

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Result 分流结果。
type Result struct {
	// Visible 模型可见内容(瘦身后)。未超预算时与传入的 content 逐字节相同。
	Visible string
	// Data 完整原文。**未分流时为空**(不重复存两份 —— 这是与 Octop 最大的差别:
	// 他们为了去重专门建了正文表,而这里 Content 已是原文,再存一遍纯属浪费)。
	Data string
	// Trimmed 是否真的分流了。
	Trimmed bool
	// OrigChars 原文字符数(按 rune 计,与 token 粗代理同口径)。
	OrigChars int
	// VisibleChars 瘦身后字符数。
	VisibleChars int
}

// SaveRatio 省下的比例(0~1;未分流 = 0)。
func (r Result) SaveRatio() float64 {
	if !r.Trimmed || r.OrigChars <= 0 {
		return 0
	}
	return 1 - float64(r.VisibleChars)/float64(r.OrigChars)
}

// Split 把过大的工具结果压进 limit(字符)预算。
//
// limit <= 0 或原文不超预算 ⇒ 原样返回(不做任何多余处理:分流只解决"太大",
// 小结果动它就是无谓的风险)。
func Split(content string, limit int) Result {
	orig := len([]rune(content))
	if limit <= 0 || orig <= limit {
		return Result{Visible: content, OrigChars: orig, VisibleChars: orig}
	}
	visible, ok := trimJSON(content, limit, orig)
	if !ok {
		visible = envelope(content, limit, orig)
	}
	return Result{
		Visible:      visible,
		Data:         content,
		Trimmed:      true,
		OrigChars:    orig,
		VisibleChars: len([]rune(visible)),
	}
}

// marker 字符串字段的中间省略标记。保留"省略了多少"是硬要求:只说"已省略"而不给量,
// 模型就无法判断是否需要换个取法。
func marker(dropped int) string {
	return fmt.Sprintf("\n…[省略 %d 字符]…\n", dropped)
}

// hint 给模型的**取用指引**。为什么必须在结果里而不是系统提示里:工具是通用的,
// 只有这里知道"是哪个结果被裁了";写进系统提示会变成每轮都付的常数成本。
const hint = "结果超过单条预算,已省略中间部分(原文仍完整保存在会话记录里,用户可在界面上展开)。要拿到被省略的内容请缩小取用范围:" +
	"shell 用 head/tail/rg -m/更窄的 pattern;file_read 用 start_line/end_line;web_fetch 换更具体的页面;搜索类工具加过滤条件。"

// headRatio 头部占比:尾部通常带错误与退出状态,但头部才是"这是什么"——
// 6:4 是折中(与 session-log 的旧结果裁剪 1200:300 的取向不同:那是历史裁剪,
// 这里模型马上就要用,信息密度在头部)。
const headRatio = 0.6

// clipRunes 保留头部 head 与尾部 tail 个 rune,中间换成省略标记;返回新串与被去掉的量。
func clipRunes(r []rune, head, tail int) (string, int) {
	if head < 0 {
		head = 0
	}
	if tail < 0 {
		tail = 0
	}
	if len(r) <= head+tail+32 {
		return string(r), 0
	}
	dropped := len(r) - head - tail
	var b strings.Builder
	b.Grow(len(r) + 64)
	b.WriteString(string(r[:head]))
	b.WriteString(marker(dropped))
	b.WriteString(string(r[len(r)-tail:]))
	return b.String(), dropped
}

// trimJSON 结构化裁剪:能解析成 JSON 对象就按字段裁,保住 count/status/exit 这类结论字段。
// 返回 ok=false 表示"这条内容不能这么裁"(非 JSON、裁完仍超预算、或字段都不是字符串)。
//
// 为什么要循环试几次:JSON 序列化会**转义膨胀**(`\n` 变 `\\n`,中文变 \uXXXX 不一定),
// 按解码后的字符量算预算会低估 —— 试算一轮就退回信封的话,恰好把最该保住的结论字段
// (shell 的 exit/count)丢掉。每次失败按比例缩预算再来,最多 4 轮。
func trimJSON(content string, limit, orig int) (string, bool) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(content), &obj); err != nil {
		return "", false
	}
	// 候选:只对可能超标的字段做,原件留住(每轮从原件重裁,不叠加裁剪)。
	strs := map[string][]rune{}
	arrs := map[string][]any{}
	for k, v := range obj {
		switch t := v.(type) {
		case string:
			if r := []rune(t); len(r) > 512 {
				strs[k] = r
			}
		case []any:
			if len(t) > 8 {
				arrs[k] = t
			}
		}
	}
	if len(strs) == 0 && len(arrs) == 0 {
		return "", false
	}
	budget := limit * 7 / 10 // 给键名/数字/转义留余量
	for attempt := 0; attempt < 4; attempt++ {
		if budget < 256 {
			return "", false
		}
		out := make(map[string]any, len(obj)+1)
		for k, v := range obj {
			out[k] = v
		}
		omitted := map[string]int{}
		for k, r := range strs {
			if len(r) <= budget {
				continue
			}
			head := int(float64(budget) * headRatio)
			clipped, dropped := clipRunes(r, head, budget-head)
			out[k] = clipped
			omitted[k] = dropped
		}
		for k, items := range arrs {
			kept, dropped, cut := trimArray(items, budget)
			if cut {
				out[k] = kept
				omitted[k] = dropped
			}
		}
		if len(omitted) == 0 {
			return "", false // 都不是"大到需要裁"的字段
		}
		out["_offloaded"] = map[string]any{
			"orig_chars": orig,
			"omitted":    omitted,
			"hint":       hint,
		}
		b, err := json.Marshal(out)
		if err != nil {
			return "", false
		}
		if n := len([]rune(string(b))); n <= limit {
			return string(b), true
		} else if attempt < 3 {
			// 超了就按实际超出量缩(不是固定比例):转义膨胀是确定的,缩一轮就到位。
			budget -= (n - limit) + 32
		}
	}
	return "", false // 键特别多/数组特别碎:交给信封
}

// trimArray 按序列化字符量保留前若干条;返回 (保留部分, 丢弃条数, 是否裁过)。
// 只处理"整条数组就是大块"的情形(元素是对象/字符串都行):逐条累加,超预算即停。
func trimArray(items []any, budget int) ([]any, int, bool) {
	used := 0
	keep := len(items)
	for i, it := range items {
		n := jsonLen(it)
		if used+n > budget && i > 0 {
			keep = i
			break
		}
		used += n
	}
	if keep >= len(items) {
		return items, 0, false
	}
	// 至少留一条:留空的数组比"少几条"更容易被误读成"没结果"。
	if keep == 0 {
		keep = 1
	}
	return items[:keep], len(items) - keep, true
}

// jsonLen 元素的序列化字符量(失败按 0 计:宁可多留一条,不当成"这条是空的")。
func jsonLen(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len([]rune(string(b)))
}

// envelope 兜底:非 JSON / 裁不进预算时的**合法 JSON 信封**(头 + 尾 + 说明)。
// 为什么不直接截断成半截文本:模型看到半截 JSON 会去猜它、甚至照着补全,
// 而"这是一个带说明的信封"是能读懂的、且不会被误解析成完整结果。
func envelope(content string, limit, orig int) string {
	r := []rune(content)
	// 骨架长度先估出来(键名 + 空串 + 引号 + 转义余量),剩下的按 6:4 分给头尾。
	scaffold := 320
	avail := limit - scaffold
	if avail < 64 {
		// 预算太小(limit < ~400):信封装不下,退化成纯头尾裁剪 + 一行说明。
		head := limit / 2
		if head < 0 {
			head = 0
		}
		clipped, _ := clipRunes(r, head, limit-head)
		return clipped
	}
	for i := 0; i < 6; i++ {
		head := int(float64(avail) * headRatio)
		tail := avail - head
		env := map[string]any{
			"_offloaded": true,
			"orig_chars": orig,
			"head":       string(r[:head]),
			"tail":       string(r[len(r)-tail:]),
			"hint":       hint,
		}
		b, err := json.Marshal(env)
		if err != nil {
			break
		}
		if len([]rune(string(b))) <= limit {
			return string(b)
		}
		// 超了(转义膨胀):按超出量缩 avail 再来一次。
		over := len([]rune(string(b))) - limit
		avail -= over + 32
		if avail < 64 {
			break
		}
	}
	clipped, _ := clipRunes(r, limit/2, limit/2)
	return clipped
}
