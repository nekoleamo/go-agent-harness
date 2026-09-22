// P4-12 T5 输入区 widget 槽位:输入行上方可注册的动态信息行(如 todo 进展/子代理活动)。
// 宿主(或未来插件)经 App.AddWidget 注册,渲染帧从 onWidgets 拉取;可经 /widgets on|off 开关。
package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// WidgetSeg 一段 widget 文本(G-E3-R2):Level 空/未知 = 默认 widget 前景色。
// 语义色档位:ok(绿) / warn·busy(琥珀) / err(红) / off(灰)。
type WidgetSeg struct {
	Text  string
	Level string
}

// RenderWidgetSegs 组装一条 widget 行:每段独立着色,段间自动回落 widget 默认前景色。
// 供宿主/插件构造语义色状态行(如「● web 已连接」);
// 纯文本亦可用(Level 置空),输出与直接拼字符串一致(无 ANSI)。
func RenderWidgetSegs(segs ...WidgetSeg) string {
	var sb strings.Builder
	for _, s := range segs {
		sb.WriteString(widgetStyle(s.Level).Render(s.Text))
	}
	return sb.String()
}

// widgetStyle 语义色档位 → 样式(未知档位回落 widget 默认色)。
func widgetStyle(level string) lipgloss.Style {
	switch level {
	case "ok":
		return lipgloss.NewStyle().Foreground(fg(TokToolOK))
	case "warn", "busy":
		return lipgloss.NewStyle().Foreground(fg(TokBusy))
	case "err":
		return lipgloss.NewStyle().Foreground(fg(TokError))
	case "off":
		return lipgloss.NewStyle().Foreground(fg(TokMeta))
	}
	return *styleWidget
}

// Widget 一条 widget 行:ID 标识,Text 每次渲染求值(返回空 = 该帧不显示)。
type Widget struct {
	ID   string
	Text func() string
}

// widgetLines 构建 widget 显示行(开关关/无注册 → 空)。
// 每行单行化(换行压空格)并截断(防撑屏;超长内容由会话流滚动查看)。
func widgetLines(s *State) []string {
	if !s.WidgetOn {
		return nil
	}
	var out []string
	for _, w := range s.Widgets {
		if w.Text == nil {
			continue
		}
		txt := w.Text()
		txt = strings.Join(strings.Fields(txt), " ")
		if txt == "" {
			continue
		}
		out = append(out, truncateVisible(txt, widgetMaxCols))
	}
	return out
}

// widgetMaxCols widget 行显示列上限(防撑屏;超长内容由会话流滚动查看)。
const widgetMaxCols = 100

// truncateVisible 按**显示列**截断(不是 rune 数):ANSI 转义序列整体跨过(不计长、不切断),
// CJK/emoji 等宽字符按 2 列计,截断时末尾追加省略号。CJK 行按 rune 计数会漏算一倍
// —— 一条“未超限”的 100 rune 中文行在 80 列终端里折成 2 行,把输入区/状态栏顶出屏幕。
// 不主动补 reset:调用方(lipgloss Render)会在行尾闭合样式(clampFrame 是例外,自己补)。
func truncateVisible(s string, max int) string {
	if max <= 1 {
		return "…"
	}
	rs := []rune(s)
	var sb strings.Builder
	n, cut := 0, false
	for i := 0; i < len(rs); {
		if rs[i] == 0x1b && i+1 < len(rs) && rs[i+1] == '[' { // CSI: ESC '[' 参数…终止字节(0x40–0x7E)
			j := i + 2
			for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
				j++
			}
			if j < len(rs) {
				j++ // 含终止字节
			}
			sb.WriteString(string(rs[i:j]))
			i = j
			continue
		}
		w := lipgloss.Width(string(rs[i]))
		if n+w > max-1 {
			cut = true
			break
		}
		sb.WriteRune(rs[i])
		n += w
		i++
	}
	if cut {
		sb.WriteString("…")
	}
	return sb.String()
}
