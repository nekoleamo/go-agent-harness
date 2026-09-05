// P4-12 T5 输入区 widget 槽位:输入行上方可注册的动态信息行(如 todo 进展/子代理活动)。
// 宿主(或未来插件)经 App.AddWidget 注册,渲染帧从 onWidgets 拉取;可经 /widgets on|off 开关。
package tui

import "strings"

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
		if n := len([]rune(txt)); n > 100 {
			txt = string([]rune(txt)[:99]) + "…"
		}
		out = append(out, txt)
	}
	return out
}
