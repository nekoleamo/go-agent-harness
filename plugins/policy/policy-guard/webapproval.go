// 网页抓取的出口审批(A7,2026-09-27 安全审计):域名级 TOFU,宿主侧裁决。
//
// 动因(F3 遗留的未闭环项):`web_fetch` 默认不在 `approval_tools` 里(读类不触发审批),
// 于是「模型把数据塞进 URL query 外发到任意外部主机」没有任何审批出口;而 `web_fetch` 的参数
// **只有 URL**,所以决策对象就是"访问哪个域名"。
//
// 档位(GAH_WEB_APPROVE):
//   - auto      **缺省**:跟随全局审批档(open/smart → 直接放行;strict → new-host)。
//   - off       完全不管(与 F3 修复前一致;自有 HTTP 代理 / 内网服务场景)
//   - new-host  每个新域名第一次访问弹一次确认,批准后记入白名单(以后不再问)
//   - query     每次都弹确认(不落白名单),适合高敏感场景
//
// 为何缺省改成 auto(2026-10-03 用户要求「智能模式不需要每次先确定再加入白名单」):
// 原来的 new-host 缺省意味着**每接触一个新域名就弹一次**,一次真实任务会碎成十几次
// 审批 —— 久而久之就变成无脑点「允许」,白名单反而越长越假。而 web_fetch 真正的风险
// 不在「去哪个域名」,而在「拿回来的内容能不能反过来支配模型」与「能不能摸到内网」,
// 这两件事已经有**不依赖审批**的硬拦:
//   - 反提示注入(F4):系统提示固定声明网页/文件/搜索结果属不可信数据;
//   - 内网与重绑定(F3):guardedDial 拒字面 IP、私网地址与 DNS 重绑定;
//   - 读凭据:凭据目录的读被内核沙箱与 ReadValidator 挡住;
//   - 响应体上限:maxBody 封顶。
//
// 剩余的真风险(去访问哪个**公网**站点)是用户自己能看见的 URL,逐域名确认的收益
// 远小于打断成本。故 open/smart 档直接放行;要拿回逐域名确认请显式 strict 或
// GAH_WEB_APPROVE=new-host。
//
// 白名单(两级,精确 host 匹配 —— 不做公后缀推断,推错的代价是放开一个不该放的域):
//   - 静态:`GAH_WEB_ALLOW_HOSTS="a.com,b.com,*.suffix.com"`(`*.suffix.com` 匹配其子域)
//   - 动态:prefs 的 `web_allow_hosts`(`$GAH_HOME/config/gah-state.json`,0600 原子写,批准后追加)
//
// 无人值守(sdk.UnattendedOf)与无确认通道一律**拒绝**(不静默放行,也不静默失败;
// 错误文案直接给出加白名单的三种办法)。
//
// 落点为何在 policy-guard 而不是 tool-web(与原方案的一处偏差,2026-09-27):审批必须拿到
// `ctx.confirm`,而**外部插件进程拿不到该服务**(host-bridge 只桥 tools/jobs/fanout,见其
// 服务表)。放工具侧会退化成"无通道即拒" —— 默认档下 `web_fetch` 对所有新域名整体不可用。
// 这里是宿主侧既有单一 pre-execute 裁决点,与 shell/工具审批同一处,不引入新协议。
package policyguard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

const (
	// webApproveEnv 出口审批档位(off / new-host / query / auto;空或非法值 = auto)。
	webApproveEnv = "GAH_WEB_APPROVE"
	// webAllowEnv 静态白名单(逗号分隔的精确 host;`*.suffix` 前缀通配)。
	webAllowEnv = "GAH_WEB_ALLOW_HOSTS"
	// webApproveAuto 跟随全局审批档(auto = 缺省口径,见文件头)。
	webApproveAuto = "auto"
)

// webApprovalTools 需要域名审批的工具 → 其 URL 参数名。
// 新增加"按 URL 抓取"的插件工具时在这里登记(与 F2 的声明优先不冲突:这是**出口**决策,
// 不是路径裁决;工具自述里没有 URL 参数位,故用宿主侧名单)。
var webApprovalTools = map[string]string{"web_fetch": "url"}

// webApproveModeOf 当前档位(空/非法 = auto 缺省)。
func webApproveModeOf() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(webApproveEnv))) {
	case "off":
		return "off"
	case "query":
		return "query"
	case "new-host":
		return "new-host"
	default:
		return webApproveAuto
	}
}

// resolveWebApproveMode 把 auto 解析成具体档:审批 open/smart → 直接放行,strict → new-host。
// 显式设过 GAH_WEB_APPROVE 的档位原样返回(env 优先于审批档)。
func resolveWebApproveMode(approval sdk.ApprovalMode) string {
	mode := webApproveModeOf()
	if mode != webApproveAuto {
		return mode
	}
	if approval == sdk.ApprovalStrict {
		return "new-host"
	}
	return "off"
}

// webAllowEntries 静态名单 + TOFU 白名单(顺序无关)。
func webAllowEntries() []string {
	var out []string
	if raw := strings.TrimSpace(os.Getenv(webAllowEnv)); raw != "" {
		out = append(out, strings.Split(raw, ",")...)
	}
	return append(out, prefs.Load().WebAllowHosts...)
}

// webHostMatch 白名单条目 vs 目标 host(均已小写)。`*.suffix` 只匹配**子域**(不含 suffix 本身)。
func webHostMatch(entry, host string) bool {
	entry = strings.ToLower(strings.TrimSpace(entry))
	switch {
	case entry == "":
		return false
	case strings.HasPrefix(entry, "*."):
		suffix := entry[1:] // ".suffix.com"
		return strings.HasSuffix(host, suffix)
	default:
		return entry == host
	}
}

// webHostAllowed 目标 host 是否已在白名单里。
func webHostAllowed(host string) bool {
	for _, e := range webAllowEntries() {
		if webHostMatch(e, host) {
			return true
		}
	}
	return false
}

// webURLArg 取工具参数 JSON 里指定字段的字符串值(非字符串/缺字段 = 空)。
func webURLArg(args, name string) string {
	if strings.TrimSpace(args) == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return ""
	}
	s, _ := m[name].(string)
	return strings.TrimSpace(s)
}

// webURLHost 从 URL 取小写 host(解析不出主机 = 空 ⇒ 调用方按安全默认拒)。
func webURLHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// webPrompt 确认文案(区分 new-host 与 query:是否会被记住)。
func webPrompt(tool, host, mode string) string {
	if mode == "query" {
		return fmt.Sprintf("%s 要访问 %s(当前为每次确认档 GAH_WEB_APPROVE=query)。允许本次吗?", tool, host)
	}
	return fmt.Sprintf("%s 要访问新域名 %s(首次)。批准后加入白名单,以后不再询问。允许吗?", tool, host)
}

// webDeniedErr 拒绝文案(必须自带放行办法:这条错误会直接给到模型与用户)。
func webDeniedErr(tool, host, why string) error {
	return fmt.Errorf("策略 guard: %s 访问域名 %s 未获批准(%s)。放行方式:① 在交互回合里批准确认弹窗;"+
		"② 无人值守或无确认通道时先加白名单 —— 环境变量 %s=%s(逗号分隔,支持 *.suffix.com),"+
		"或在 $GAH_HOME/config/gah-state.json 的 web_allow_hosts 里追加;③ 关掉该门:%s=off。",
		tool, host, why, webAllowEnv, host, webApproveEnv)
}

// checkWebToolFetch 单次工具调用的出口裁决(非抓取工具 = 直接放行)。
//
// 顺序:解析后的档位 off → 白名单命中 → 无人值守 → 确认通道 → 弹确认(批准后按档记白名单)。
// approval = 本次生效的审批档(auto 档据此决定要不要问,见 resolveWebApproveMode)。
func checkWebToolFetch(ctx context.Context, confirm sdk.ConfirmService, tool, args string, approval sdk.ApprovalMode) error {
	argName, ok := webApprovalTools[tool]
	if !ok {
		return nil
	}
	raw := webURLArg(args, argName)
	if raw == "" {
		return nil // 缺 URL:工具自己会报错,不在这里抢答
	}
	host := webURLHost(raw)
	if host == "" {
		return fmt.Errorf("策略 guard: %s 的 URL 解析不出主机名(%q),已按安全默认拒绝;"+
			"请给出完整 http(s) URL。", tool, raw)
	}
	mode := resolveWebApproveMode(approval)
	if mode == "off" || webHostAllowed(host) {
		return nil
	}
	if sdk.UnattendedOf(ctx) {
		return webDeniedErr(tool, host, "本次为无人值守运行,不弹确认")
	}
	if confirm == nil {
		return webDeniedErr(tool, host, "当前没有确认通道(ctx.confirm 未装配)")
	}
	yes, err := confirm.Confirm(ctx, webPrompt(tool, host, mode))
	if err != nil {
		// 用户按了停止 → 中止而非拒绝(文案区别见 sdk/aborted.go):不挂错就只会得到
		// 一条红色的「blocked: 策略 guard: 网页抓取确认失败: context canceled」。
		if ctx.Err() != nil {
			return sdk.AbortedError("策略 guard: %s 未执行(等待域名确认时被停止)", tool)
		}
		return fmt.Errorf("策略 guard: 网页抓取确认失败: %w", err)
	}
	if !yes {
		return fmt.Errorf("策略 guard: 用户拒绝了 %s 访问域名 %s。", tool, host)
	}
	if mode == "new-host" {
		prefs.AddWebAllowHost(host) // 批准即记入白名单(下次直接放行)
	}
	return nil
}
