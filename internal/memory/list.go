// list.go:记忆的**展示与筛选**(命令面 /memory list 与 Web 面板共用同一份实现)。
//
// 为什么不各写一份:展示行是"用户看到的事实"(序号、类型、来源),命令面与面板必须逐字一致 ——
// 两份实现迟早在"序号对不上"或"来源漏了"上分叉(第一百一十批就吃过这个亏)。
package memory

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"
)

// ListOptions 筛选条件(零值 = 全部,新的在前)。
type ListOptions struct {
	Kind      string    // 只留该类型(preference/convention/decision/pitfall/env)
	AutoOnly  bool      // 只看自动写入的:判据 = 带 kind 元数据(人工 add 的条目没有)
	StaleDays int       // 只留**超过**这么多天的(0 = 不筛)
	Now       time.Time // StaleDays 的基准(零值 = 现在)
}

// ContentID 条目内容的稳定短标识(与检索层的 Item.ID 同一算法)。
//
// 用途:superseded_by 要指向"被取代的那条",而 markdown 条目没有数据库主键;
// 内容哈希在"内容不变"时稳定,且删掉别的条不会让它失效。
func ContentID(content string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(content))))
	return fmt.Sprintf("%08x", h.Sum32())
}

// Active 去掉**已被取代**的条目(行尾 superseded_by 指向它)。
//
// 为什么单向标记就够:新条目声明"我取代了谁",旧文件一个字都不用改 —— 改旧行意味着
// 整体重写文件(有丢失风险),而"被取代"这件事由新条目单方面声明已经足够表达。
func Active(entries []Entry) []Entry {
	superseded := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.SupersededBy != "" {
			superseded[e.SupersededBy] = true
		}
	}
	if len(superseded) == 0 {
		return entries
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if superseded[ContentID(e.Content)] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// FilterEntries 按条件筛选(顺序:去被取代 → 类型 → 自动写入 → 超期),返回**展示序**。
func FilterEntries(entries []Entry, opt ListOptions) []Entry {
	now := opt.Now
	if now.IsZero() {
		now = time.Now()
	}
	live := Active(entries)
	var cutoff time.Time
	if opt.StaleDays > 0 {
		cutoff = now.AddDate(0, 0, -opt.StaleDays)
	}
	kept := make([]Entry, 0, len(live))
	for _, e := range live {
		if opt.Kind != "" && e.Kind != opt.Kind {
			continue
		}
		if opt.AutoOnly && e.Kind == "" {
			continue
		}
		if !cutoff.IsZero() {
			d, err := time.Parse("2006-01-02", e.Date)
			// 日期解析不了的老条目:不算"超期"(宁可漏,也不要把格式问题当成过期)。
			if err != nil || !d.Before(cutoff) {
				continue
			}
		}
		kept = append(kept, e)
	}
	return SortedForDisplay(kept)
}

// DisplayLines 渲染展示行(序号 + 日期 + 正文 + 类型/置信度 + 来源)。
// 序号基于**展示序**(1 起),与 `/memory rm <序号>` 的语义一致。
func DisplayLines(entries []Entry, opt ListOptions) []string {
	es := FilterEntries(entries, opt)
	out := make([]string, 0, len(es))
	for i, e := range es {
		line := fmt.Sprintf("%d. [%s] %s", i+1, e.Date, e.Content)
		if e.Kind != "" {
			line += " (类型 " + e.Kind
			if e.Confidence > 0 {
				line += " " + FormatConfidence(e.Confidence)
			}
			line += ")"
		}
		if e.SupersededBy != "" {
			line += " (已更新旧条目)"
		}
		if e.Source != "" {
			line += " (来源: 会话 " + e.Source + ")"
		}
		out = append(out, line)
	}
	return out
}

// BestMatch 文本与既有条目的最大相似度,并返回那条内容(空 = 没有可比的)。
func BestMatch(text string, items []Item) (float64, string) {
	var best float64
	var target string
	for _, it := range items {
		if s := Similarity(text, it.Content); s > best {
			best, target = s, it.Content
		}
	}
	return best, target
}
