// 语义色 token 化(P4-9/E2):渲染用色的单一事实源。
// 界面全部颜色按角色收口为 Token,样式一律经 fg()/DefaultPalette 派生——
// 本文件之外零色值字面量。换肤 = 只改 DefaultPalette 本表(改表即整体重配色),
// 渲染层代码不动。默认表值即既有行为基线(256 色索引),不逐字等价不达验收。
package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Token 语义色 token:按界面角色命名;表值 = 256 色索引字符串。
type Token string

const (
	// —— 会话流内容行(kind 基础色,styleForKind)——
	TokUser      Token = "user"      // 用户行:青
	TokAssistant Token = "assistant" // 助手回复:绿
	TokTool      Token = "tool"      // 工具调用/结果:琥珀
	TokMeta      Token = "meta"      // 系统/切换提示等:灰
	TokError     Token = "error"     // 错误横幅:红

	// —— 输入/命令/选择器 ——
	TokPrompt Token = "prompt" // 输入提示符:品红
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
	TokMdKey   Token = "md-key"   // 代码块内关键字:淡紫
	TokMdStr   Token = "md-str"   // 代码块内字符串:暖橙
	TokMdCmt   Token = "md-cmt"   // 代码块内注释:灰

	// —— 搜索高亮 ——
	TokSearchBg    Token = "search-bg"     // 命中行背景:暗
	TokSearchCurBg Token = "search-cur-bg" // 当前命中背景:琥珀
)

// DefaultPalette 默认调色板(token → 256 色索引)。
// 值为既有渲染基线(对齐历史逐字输出),勿改动;新换肤 = 在此覆盖/替换后重建样式。
var DefaultPalette = map[Token]string{
	TokUser: "81", TokAssistant: "120", TokTool: "220", TokMeta: "245",
	TokError: "203", TokPrompt: "207", TokPick: "207", TokCursor: "214",

	TokStatus: "250", TokBusy: "214",

	TokBarThumb: "214", TokBarTrack: "240", TokBarHover: "172", TokBarEnd: "214",

	TokMdCode: "179", TokMdBold: "231", TokMdTitle: "51", TokMdList: "220", TokMdHr: "245",
	TokMdKey: "141", TokMdStr: "215", TokMdCmt: "244",

	TokSearchBg: "238", TokSearchCurBg: "214",
}

// fg 取 token 对应前景色(样式构造统一入口;缺 token 回退无色)。
func fg(t Token) color.Color {
	return lipgloss.Color(DefaultPalette[t])
}
