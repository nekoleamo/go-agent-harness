// P4-2 @文件引用补全:输入中以空白/行首开头的 "@词"(光标在词内/尾)激活候选列表
// (项目文件路径,Value=相对路径),↑/↓ 移动、Tab/Enter 应用替换整段 @token;
// Esc 关闭(保留文本)。状态机纯逻辑,文件候选由 App 注入(onFiles,含缓存)。
package tui

import (
	"strings"
	"unicode"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Mention @ 引用候选状态。Start = token 起始 rune 下标(该处字符恒为 '@'),光标
// 位于 [Start, Cursor) 内任意处(输入可继续扩展 token);Items = All 的子串过滤子集。
type Mention struct {
	Start  int
	Items  []sdk.Option
	All    []sdk.Option // 项目文件全量候选(重过滤/退格恢复)
	Cursor int
}

// mentionTokenAt 光标前是否处于可引用段:@ 开头的连续非空白词(词前为行首或空白;
// URL/邮箱内 @ 前非空白 → 不触发)。返回 @ 的 rune 下标(ok 时才有效)。
func mentionTokenAt(rs []rune, cursor int) (start int, ok bool) {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(rs) {
		cursor = len(rs)
	}
	i := cursor - 1
	for i >= 0 && !unicode.IsSpace(rs[i]) {
		i--
	}
	seg := i + 1
	if seg < cursor && seg < len(rs) && rs[seg] == '@' {
		return seg, true
	}
	return 0, false
}

// mentionToken 当前 token 文本(含 @;nil mention 返回 "")。
func (s *State) mentionToken() string {
	if s.Mention == nil {
		return ""
	}
	rs := []rune(s.Input)
	if s.Mention.Start < 0 || s.Mention.Start > len(rs) || s.Cursor < s.Mention.Start || s.Cursor > len(rs) {
		return ""
	}
	return string(rs[s.Mention.Start:s.Cursor])
}

// syncMention 输入/光标变化后刷新 @ 引用候选(纯状态,不改文本):
// 触发条件 = 光标位于 @ 词段(见 mentionTokenAt)且非命令输入、无命令选择器/自由向导。
// token 空(仅 "@")列出全部候选(供浏览/提示继续输入);token 变化按子串过滤(复用 filterOptions)。
func (s *State) syncMention(all []sdk.Option) {
	// 命令/选择器/向导态不启用 @(命令文本保持简单语义)
	if s.Pick != nil || s.Free != nil || strings.HasPrefix(s.Input, "/") {
		s.Mention = nil
		return
	}
	rs := []rune(s.Input)
	start, ok := mentionTokenAt(rs, s.Cursor)
	if !ok {
		s.Mention = nil
		return
	}
	token := string(rs[start:s.Cursor])
	if s.Mention == nil {
		// 新激活:重建候选(All 为当前文件全量)
		s.Mention = &Mention{Start: start, All: all, Cursor: 0}
	}
	if s.Mention.Start != start {
		s.Mention.Start = start // 光标在段内移动:起点保持 @ 位置(恒同)
	}
	// token 去掉 '@' 做过滤(空 = 全部候选)
	q := strings.TrimPrefix(token, "@")
	items := s.Mention.All
	if q != "" {
		items = filterOptions(s.Mention.All, q)
	}
	s.Mention.Items = items
	if s.Mention.Cursor >= len(items) {
		s.Mention.Cursor = 0 // 过滤变化光标钳制(保留 0 便于 Tab 直取首项)
	}
}

// applyMention 用选中文项替换 @token 整段(区间 [Start, Cursor)),光标移到插入路径后。
// 无候选(空列表)时不动(防误替换);返回是否应用。
func (s *State) applyMention(choice string) bool {
	if s.Mention == nil || len(s.Mention.Items) == 0 {
		return false
	}
	if choice == "" {
		choice = s.Mention.Items[0].Value // 防御:空值取首项
	}
	rs := []rune(s.Input)
	start := s.Mention.Start
	if start < 0 || start > s.Cursor || s.Cursor > len(rs) {
		s.Mention = nil
		return false
	}
	pre := rs[:start]
	post := rs[s.Cursor:]
	s.Input = string(pre) + choice + string(post)
	s.Cursor = start + len([]rune(choice))
	s.Mention = nil
	s.vActive = false
	return true
}
