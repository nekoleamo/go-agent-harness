// `gah im --status` headless 连接探活(E 组 E4):给脚本/CI 一个可判定的退出码。
//
//	gah im --status [--channel wechat|qq] [--json]
//
// 退出码:0 = 在线 / 3 = 未连接或未配置 / 4 = 凭证无效(需重新授权/重新填写) / 5 = 装配缺失(无 ui-im-*)。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 退出码(§4.4 冻结)。
const (
	imExitOnline    = 0
	imExitOffline   = 3
	imExitBadCreds  = 4
	imExitNoChannel = 5
)

// imStatusReport 打印渠道连接状态并返回退出码。
// 优先用 IMConnectService(E0 统一契约,相位更细);缺失时回退 IMChannelService(只读状态)。
func imStatusReport(c sdk.Ctx, channel string, asJSON bool) int {
	var conn sdk.IMConnectService
	if err := c.Inject("ctx.imChannels", &conn); err != nil || conn == nil {
		// 通道未装配:显式 5(脚本可区分「没装」与「没连」)
		msg := "IM 通道未装配(profile 无 ui-im-*;用 gah im / gah im-qq 启动对应 profile)"
		if asJSON {
			out, _ := json.Marshal(map[string]any{"error": msg})
			fmt.Println(string(out))
		} else {
			fmt.Fprintln(os.Stderr, "gah im --status:", msg)
		}
		return imExitNoChannel
	}
	st := conn.ConnectStatus()
	spec := conn.ConnectSpec()
	if channel != "" && !strings.EqualFold(channel, st.Channel) {
		msg := fmt.Sprintf("渠道不匹配:当前 profile 为 %s,请求 %s", st.Channel, channel)
		if asJSON {
			out, _ := json.Marshal(map[string]any{"error": msg, "status": st})
			fmt.Println(string(out))
		} else {
			fmt.Fprintln(os.Stderr, "gah im --status:", msg)
		}
		return imExitNoChannel
	}
	if asJSON {
		out, _ := json.Marshal(map[string]any{"status": st, "spec": spec})
		fmt.Println(string(out))
	} else {
		fmt.Printf("%s\t%s\t%s\n", st.Channel, st.Phase, firstNonEmpty(st.Detail, st.Error, "-"))
		if st.Account != "" {
			fmt.Printf("  账号: %s\n", st.Account)
		}
		if st.Error != "" {
			fmt.Printf("  错误: %s\n", st.Error)
		}
		if spec.Kind == sdk.IMConnectForm {
			fmt.Printf("  连接方式: 表单(AppID/AppSecret)%s\n", hintOf(spec))
		} else if spec.Kind == sdk.IMConnectQR {
			fmt.Printf("  连接方式: 扫码%s\n", hintOf(spec))
		}
	}
	switch {
	case st.Phase == sdk.IMPhaseDone:
		return imExitOnline
	case st.Phase == sdk.IMPhaseFailed && looksLikeBadCreds(st.Error):
		return imExitBadCreds
	default:
		return imExitOffline
	}
}

func hintOf(spec sdk.IMConnectSpec) string {
	if spec.LoginURL == "" {
		return ""
	}
	return "(" + spec.LoginURL + ")"
}

// looksLikeBadCreds 凭证无效类错误判定(脱敏文案匹配;仅用于退出码,不参与业务)。
func looksLikeBadCreds(msg string) bool {
	m := strings.ToLower(msg)
	for _, kw := range []string{"token", "凭证", "secret", "appid", "401", "403", "invalid", "无效", "过期", "auth"} {
		if strings.Contains(m, kw) {
			return true
		}
	}
	return false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
