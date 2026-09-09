// 语义色 token 化(P4-9/E2):渲染用色的单一事实源。
// 界面全部颜色按角色收口为 Token,样式一律经 fg()/DefaultPalette 派生——
// 本文件之外零色值字面量。换肤 = 只改 DefaultPalette 本表(改表即整体重配色),
// 渲染层代码不动。默认表值即既有行为基线(256 色索引),不逐字等价不达验收。
package tui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// Token 语义色 token:按界面角色命名;表值 = 256 色索引字符串或 #hex。
type Token string

const (
	// —— 会话流内容行(kind 基础色,styleForKind)——
	TokUser      Token = "user"      // 用户行:青
	TokAssistant Token = "assistant" // 助手回复:绿
	TokTool      Token = "tool"      // 工具调用/结果:琥珀
	TokThinking  Token = "thinking"  // 思维块显示(推理过程):灰斜体
	TokToolOK    Token = "tool-ok"   // 工具结果成功行:绿
	TokMeta      Token = "meta"      // 系统/切换提示等:灰
	TokError     Token = "error"     // 错误横幅:红

	// —— 消息/工具背景块(P5-V2/V3,对齐 pi userMessageBg/toolPendingBg 语义)——
	TokUserBg   Token = "user-bg"    // 用户消息整行背景(深灰,黑底上略提亮)
	TokToolBg   Token = "tool-bg"    // 工具调用行(pending)背景:深灰
	TokToolOKBg Token = "tool-ok-bg" // 工具成功结果行背景:中灰(结果比调用亮一档)
	TokToolErrBg Token = "tool-err-bg" // 工具失败结果行背景:中灰(红字表语义)

	// —— 输入/命令/选择器 ——
	TokPrompt Token = "prompt" // 输入提示符:品红
	TokWidget Token = "widget" // 输入区上方 widget 行:浅灰(次要信息)
	TokPick   Token = "pick"   // 选择器高亮行:品红
	TokCursor Token = "cursor" // 输入块光标:琥珀

	// —— 状态栏 ——
	TokStatus Token = "status" // 空闲态文字:浅灰
	TokBusy   Token = "busy"   // 运行中高亮(思考/执行):琥珀

	// —— 滚动条 ——
	TokBarThumb Token = "barthumb" // 滑块:琥珀
	TokBarTrack Token = "bartrack" // 轨道:灰
	TokBarHover Token = "barhover" // 悬停高亮:亮琥珀
	TokBarEnd   Token = "barend"   // 回底指示(▼):琥珀

	// —— markdown 轻渲染 ——
	TokMdCode  Token = "md-code"  // 行内 code 与代码块前景:暗金
	TokMdBold  Token = "md-bold"  // 粗体:亮白
	TokMdTitle Token = "md-title" // 标题:青
	TokMdList  Token = "md-list"  // 列表符:琥珀
	TokMdHr    Token = "md-hr"    // 分隔线:灰
	TokMdLink  Token = "md-link"  // 链接文本 [text](url):gruvbox accent 蓝灰
	TokMdQuote Token = "md-quote" // 引用行(> 前缀):gruvbox muted 米白
	TokMdKey   Token = "md-key"   // 代码块内关键字:淡紫
	TokMdStr   Token = "md-str"   // 代码块内字符串:暖橙
	TokMdCmt   Token = "md-cmt"   // 代码块内注释:灰

	// —— 代码块精细语法着色(P5-V4,补 5 色对齐 gruvbox syntax 语义)——
	TokSyntaxVar    Token = "syntax-var"    // 变量:textSoft 米白
	TokSyntaxNum    Token = "syntax-num"    // 数字:orange
	TokSyntaxType   Token = "syntax-type"   // 类型/常量:cyan
	TokSyntaxOp     Token = "syntax-op"     // 运算符:accent2 绿
	TokSyntaxPunct  Token = "syntax-punct"  // 标点:dim

	// —— 输入区左缘竖线(P5-V5,按思考等级着色,对齐 pi thinking 边框色)——
	TokThinkOff  Token = "think-off"  // 思考 off:灰
	TokThinkLow  Token = "think-low"  // low:accent 蓝灰
	TokThinkMed  Token = "think-med"  // medium:accent2 绿
	TokThinkHigh Token = "think-high" // high:purple 紫

	// —— 搜索高亮 ——
	TokSearchBg    Token = "search-bg"     // 命中行背景:暗
	TokSearchCurBg Token = "search-cur-bg" // 当前命中背景:琥珀

	// —— 工具结果 diff 轻染色(P4-7)——
	TokDiffAdd Token = "diff-add" // 新增行(+):淡绿
	TokDiffDel Token = "diff-del" // 删除行(-):暗红
	TokDiffHdr Token = "diff-hdr" // 文件/块头(+++ --- @@):灰
)

// DefaultPalette 默认调色板(token → 256 色索引)。
// M15 pi 式默认样式(2026-09):assistant 恢复为接近默认前景的亮白(整行绿曾过重,
// 克制层次由 markdown 局部高亮承担),状态栏/工具行/辅助信息统一降饱和。
// 仍为单一事实源:改表即整体重配色;换肤覆盖经 ApplyTheme,渲染层零改动。
// 默认调色板。既有 token 值 = M15 基线不动;新增 token(P5 视觉升级)对齐
// gruvbox-dark 色系:背景三类灰阶(黑底上层次:调用 235 < 用户 236 < 结果 237),
// md 链接/引用/思维边框/语法用 gruvbox 原 hex 保真。
var DefaultPalette = map[Token]string{
	TokUser: "81", TokAssistant: "253", TokTool: "246", TokToolOK: "114", TokMeta: "245",
	TokThinking: "242", TokError: "203", TokPrompt: "207", TokPick: "207", TokCursor: "214",

	TokUserBg: "236", TokToolBg: "235", TokToolOKBg: "237", TokToolErrBg: "237",

	TokStatus: "252", TokBusy: "214", TokWidget: "243",

	TokBarThumb: "214", TokBarTrack: "240", TokBarHover: "172", TokBarEnd: "214",

	TokMdCode: "179", TokMdBold: "231", TokMdTitle: "51", TokMdList: "220", TokMdHr: "245",
	TokMdLink: "#83a598", TokMdQuote: "#d5c4a1",
	TokMdKey: "141", TokMdStr: "215", TokMdCmt: "244",

	TokSyntaxVar: "#ebdbb2", TokSyntaxNum: "#fe8019", TokSyntaxType: "#8ec07c",
	TokSyntaxOp: "#8ec07c", TokSyntaxPunct: "#bdae93",

	TokThinkOff: "#665c54", TokThinkLow: "#83a598", TokThinkMed: "#8ec07c", TokThinkHigh: "#d3869b",

	TokSearchBg: "238", TokSearchCurBg: "214",

	TokDiffAdd: "114", TokDiffDel: "167", TokDiffHdr: "245",
}

// active 当前生效调色板(默认克隆 DefaultPalette;主题覆盖经 ApplyTheme;
// token 缺失或值为空自动回落默认表)。渲染查询一律经 colorVal——换肤只改覆盖层,
// 渲染层零改动。
var active = clonePalette(DefaultPalette)

func clonePalette(base map[Token]string) map[Token]string {
	m := make(map[Token]string, len(base))
	for k, v := range base {
		m[k] = v
	}
	return m
}

// colorVal 当前生效色值(token 缺失/空值回落 DefaultPalette)。
func colorVal(t Token) string {
	if v, ok := active[t]; ok && v != "" {
		return v
	}
	return DefaultPalette[t]
}

// ApplyTheme 应用主题覆盖(token 名 → 色值;256 色索引数字或 #hex)。
// 可部分覆盖(未覆盖 token 保持既有值);空值 = 回落默认表;
// 未知 token 名/非法色值显式报错(不静默忽略)。
func ApplyTheme(overrides map[string]string) error {
	for name, v := range overrides {
		tok := Token(name)
		if _, ok := DefaultPalette[tok]; !ok {
			return fmt.Errorf("palette: 未知语义色 token %q(合法名见 Token 常量)", name)
		}
		if v != "" && !validColorValue(v) {
			return fmt.Errorf("palette: 非法色值 %q(token %q;支持 256 索引数字或 #hex)", v, name)
		}
	}
	for name, v := range overrides {
		active[Token(name)] = v
	}
	return nil
}

// ResetTheme 重置当前调色板 = DefaultPalette(/theme default 与测试隔离用)。
func ResetTheme() { active = clonePalette(DefaultPalette) }

// ThemeSnapshot 当前生效调色板快照(token → 色值;测试/展示用)。
func ThemeSnapshot() map[Token]string { return clonePalette(active) }

// validColorValue 色值合法性:256 色索引数字或 #hex(3/6 位)。
func validColorValue(v string) bool {
	if strings.HasPrefix(v, "#") {
		h := v[1:]
		if len(h) != 3 && len(h) != 6 {
			return false
		}
		for _, c := range h {
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return false
			}
		}
		return true
	}
	n, err := strconv.Atoi(v)
	return err == nil && n >= 0 && n <= 255
}

// fg 取 token 对应前景色(样式构造统一入口;经当前生效调色板)。
func fg(t Token) color.Color {
	return lipgloss.Color(colorVal(t))
}
