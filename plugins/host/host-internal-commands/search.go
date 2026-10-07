// 一条只读诊断命令:/websearch —— 回答「我现在在用哪家搜索、端点哪个、key 有没有」。
//
// 为什么值得单开一条命令(2026-10-07 用户排查 web_search 报错时花掉的功夫):
// 报错只有一句「搜索服务返回 402」或「未知搜索 provider "anysearch"」,既没说是哪家、
// 也没说怎么查。而「是哪家」只能翻 gah-data/config/search.yaml —— 那份文件带凭据(围栏拦)、
// 路径里还有空格(`~/Library/Application Support/…`),于是用户先怀疑「是不是改错文件了」。
//
// 这一层不提供「换 provider」的能力:换 provider 属于设置面,改配置才是。
//
// 名字:叫 /websearch 而非 /search —— 后者是 TUI 专属的 UI 元素搜索命令,同名会让两处帮助含混。
package hostintcmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// cmdSearch /search [试搜 <词>] —— 报告搜索配置现状,可选实跑一次。
//
// 用法:
//
//	/search          只看配置(纯本地读,不发请求)
//	/search 测试 财报 顺便实跑一次,看通路与耗时
func (h *Host) cmdSearch(args []string) (string, error) {
	var svc sdk.SearchService
	if err := h.c.Inject("ctx.search", &svc); err != nil || svc == nil {
		return "", errString("搜索能力未就绪(tool-web 未加载?可经 /plugins 看它的状态)")
	}
	info := svc.SearchInfo()

	// 带词 ⇒ 实跑一次(用户要的是「到底能不能用」,不是「配置像不像对的」)
	var query string
	if len(args) > 1 && (args[0] == "测试" || args[0] == "试搜" || args[0] == "test") {
		query = strings.Join(args[1:], " ")
	}
	if query != "" {
		info = svc.TryProbe(context.Background(), query, 3)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "搜索配置(只读)\n")
	fmt.Fprintf(&b, "  provider  %s\n", info.Provider)
	if info.EndpointHost != "" {
		fmt.Fprintf(&b, "  端点      %s\n", info.EndpointHost)
	}
	switch {
	case info.HasKey:
		fmt.Fprintf(&b, "  key       已配置(%s)\n", info.ConfiguredKeyMask)
	default:
		b.WriteString("  key       未配置(匿名调用)\n")
	}
	if info.Note != "" {
		fmt.Fprintf(&b, "  注意      %s\n", info.Note)
	}

	if query != "" {
		fmt.Fprintf(&b, "\n实跑一次 %q:%d ms\n", query, info.LatencyMS)
		switch {
		case info.Err != "":
			fmt.Fprintf(&b, "  结果      失败:%s\n", info.Err)
		case info.Ok:
			fmt.Fprintf(&b, "  结果      通(%d 条)\n", info.ResultCount)
			for _, t := range info.ResultTitles {
				fmt.Fprintf(&b, "            · %s\n", t)
			}
		}
	} else {
		b.WriteString("\n加个词可实跑一次:/websearch 测试 财报\n")
	}
	return b.String(), nil
}
