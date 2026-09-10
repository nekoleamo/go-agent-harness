// G-E3-R2:TUI IM 状态 widget —— 输入行上方一行「● 微信 已连接 · ● QQ(沙箱) 在线」。
//
// 设计:
//   - 懒解析 ctx.imChannels(UI 插件与 IM 通道插件无拓扑依赖,启动期注入会恒 nil;
//     与 web 面板 / policy-guard 同一时序纪律),未装配或无渠道 → 返回空串(widget 行不显示);
//   - 每渲染帧一次 map 查找 + 只读状态,零阻塞零 IO(状态内部已加锁);
//   - 语义色经 tui.RenderWidgetSegs 分段着色(绿=已连接 / 琥珀=运行中或需处理 / 灰=未连接);
//   - 渠道命令(/im 聚合总览、/wechat、/qq)仍是完整能力面,本行只做「一眼可见」。
package uitui

import (
	"github.com/nekoleamo/go-agent-harness/sdk"
	"github.com/nekoleamo/go-agent-harness/tui"
)

// imStatusWidget 返回 widget 求值函数(AddWidget 注册)。
func imStatusWidget(c sdk.Ctx) func() string {
	return func() string {
		var ic sdk.IMChannelService
		if err := c.Inject("ctx.imChannels", &ic); err != nil || ic == nil {
			return ""
		}
		sts := ic.Status()
		if len(sts) == 0 {
			return ""
		}
		env := ""
		if cs, ok := ic.(sdk.IMConnectService); ok {
			env = cs.ConnectStatus().Env
		}
		segs := make([]tui.WidgetSeg, 0, len(sts)*5)
		for i, st := range sts {
			if i > 0 {
				segs = append(segs, tui.WidgetSeg{Text: " · "})
			}
			level := imStateLevel(st.State)
			channel := imChannelLabel(st.Channel)
			if e := imEnvLabel(env); e != "" {
				channel += "(" + e + ")"
			}
			segs = append(segs,
				tui.WidgetSeg{Text: imStateDot(level), Level: level},
				tui.WidgetSeg{Text: " " + channel},
				tui.WidgetSeg{Text: " " + imStateText(st.State), Level: level},
			)
			if st.Error != "" {
				segs = append(segs, tui.WidgetSeg{Text: " 需处理", Level: "warn"})
			}
		}
		return tui.RenderWidgetSegs(segs...)
	}
}

// imChannelLabel 渠道显示名(未知渠道原样)。
func imChannelLabel(ch string) string {
	switch ch {
	case "wechat":
		return "微信"
	case "qq":
		return "QQ"
	}
	return ch
}

// imStateText 渠道状态文案(sdk.IMChannelStatus.State)。
func imStateText(state string) string {
	switch state {
	case "online":
		return "已连接"
	case "running":
		return "运行中"
	case "configuring":
		return "未配置"
	}
	return "未连接"
}

// imStateLevel 状态 → widget 语义色档位。
func imStateLevel(state string) string {
	switch state {
	case "online":
		return "ok"
	case "running":
		return "warn"
	}
	return "off"
}

// imStateDot 语义色档位 → 状态点(与色盲可辨识的形状差异配合)。
func imStateDot(level string) string {
	switch level {
	case "ok":
		return "●"
	case "warn":
		return "◐"
	}
	return "○"
}

// imEnvLabel 连接环境显示名(仅 form 渠道给 Env;空 = 不展示)。
func imEnvLabel(env string) string {
	switch env {
	case "sandbox":
		return "沙箱"
	case "official":
		return "正式"
	}
	return ""
}
