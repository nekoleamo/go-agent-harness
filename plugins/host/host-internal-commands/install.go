// /install 斜杠命令(2026-10-03):插件**安装与信任**。
//
// 与 `/plugins` 的分工(两条命令的 Desc 里都写明,免得用户在「装不上」和「装不上之后不加载」
// 两件事之间来回猜):
//   - `/plugins` 管**启停**:进程内插件的加载/卸载与配置默认。
//   - `/install` 管**安装与信任**:外部进程插件的落位、白名单登记、卸载。
//
// 为什么 TUI 也要有:安装能力此前只在 CLI(`gah -install`)里,而用户多数时候在 TUI 里
// 操作 —— 「开个终端敲命令」是断链。三个入口共用 internal/install 同一份内核与同一组
// 安全约束(审批档门 + 二次确认文案),而不是各写一份。
package hostintcmd

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/install"
	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// cmdInstall /install 的实现。
//
// 二次确认走 ctx.confirm(与危险命令审批同一通道):命令面本来就有它,而 Web 侧的面板确认
// 是 UI 行为(服务端只接受 `confirmed:true`)。两条路径的**文案**来自同一个
// install.ConfirmPrompt,所以用户在哪儿看到的都是同一句话。
func (h *Host) cmdInstall(args []string) (string, error) {
	home := h.pluginHome()
	if len(args) < 1 || args[0] == "list" {
		return installRows(home)
	}
	if err := h.guardInstallApproval(); err != nil {
		return "", errString(err.Error())
	}
	switch args[0] {
	case "check":
		// 检查更新:**只问不装**(批一 §1.5)。网络只在这里发生一次,发 git ls-remote,不 clone。
		return installCheck(home)
	case "uninstall":
		if len(args) < 2 {
			return "", errString("/install uninstall <id>")
		}
		facts := install.ConfirmFacts{ID: args[1], Dir: filepath.Join(home, "plugins", args[1]), Uninstall: true}
		ok, err := h.askInstall(facts)
		if err != nil {
			return "", err
		}
		if !ok {
			return "已取消卸载 " + args[1], nil
		}
		if err := install.Uninstall(args[1], home, h.externalPlugins()); err != nil {
			return "", errString(err.Error())
		}
		return "已卸载 " + args[1] + "(进程已停,白名单与来源账条目一并撤销)", nil
	case "disable":
		if len(args) < 2 {
			return "", errString("/install disable <二进制名>")
		}
		return h.pluginLifecycle(args[1], false)
	case "enable":
		if len(args) < 2 {
			return "", errString("/install enable <二进制名>")
		}
		return h.pluginLifecycle(args[1], true)
	case "trust":
		if len(args) < 2 {
			return "", errString("/install trust <名>")
		}
		ok, err := h.askInstall(install.ConfirmFacts{
			ID: args[1], Source: filepath.Join(home, "plugins"),
			BuildCmd: "登记当前盘上那一份的 sha256(不改文件)",
		})
		if err != nil {
			return "", err
		}
		if !ok {
			return "已取消登记 " + args[1], nil
		}
		if err := install.Trust(args[1], home); err != nil {
			return "", errString(err.Error())
		}
		return "已把 " + args[1] + " 登记进哈希白名单(下次调用即生效)", nil
	case "untrust":
		if len(args) < 2 {
			return "", errString("/install untrust <名>")
		}
		if err := install.Untrust(args[1], home); err != nil {
			return "", errString(err.Error())
		}
		return "已从白名单移除 " + args[1], nil
	case "artifact":
		// /install artifact <url> <id> <name>:装一个别人已构建好的产物(免 Go)。
		// 位置参数而非旗标:斜杠命令的参数是位置式的,塞旗标会让这条命令同时有两种身份。
		if len(args) < 4 {
			return "", errString("/install artifact <url> <插件id> <tool-xxx>")
		}
		return h.installArtifact(args[1], args[2], args[3], home)
	case "prebuilt":
		// /install prebuilt <repo>:走预编译产物(不跑构建)。
		// 单独一个子命令而不是 `/install <repo> --prebuilt`:斜杠命令的参数里塞旗标
		// 会让它同时有两种身份(位置参数与旗标),而这里两者的确认文案完全不同。
		if len(args) < 2 {
			return "", errString("/install prebuilt <仓库>[@版本]")
		}
		return h.installSpec(args[1], home, true)
	default:
		return h.installSpec(strings.TrimSpace(args[0]), home, false)
	}
}

// installSpec 装一个 spec(prebuilt = 走预编译产物)。
func (h *Host) installSpec(spec string, home string, prebuilt bool) (string, error) {
	facts := install.Preview(spec, home)
	if prebuilt {
		if f, ok := install.PreviewPrebuilt(spec, home); ok {
			facts = f
		}
	}
	ok, err := h.askInstall(facts)
	if err != nil {
		return "", err
	}
	if !ok {
		return "已取消安装 " + spec, nil
	}
	res, err := install.InstallWithOpts(spec, home, install.InstallOpts{Prebuilt: prebuilt})
	if err != nil {
		return "", errString(err.Error())
	}
	out := "已安装 " + res.ID + " → " + res.Dir
	if res.Prebuilt != "" {
		out += "\n产物:" + res.Prebuilt + "(作者发布的预编译产物,**未执行任何构建命令**)"
	} else if res.BuildCmd != "" {
		out += "\n构建命令:" + res.BuildCmd
	}
	if res.BuildImplicit {
		out += "\n注意:该仓库的 plugin.yaml **没有声明 build:** —— 上面那条命令是 gah 替你选的,不是作者写的"
	}
	if res.Tidied {
		// 补依赖意味着「装这个插件往供应链面里加了仓库没声明的模块」—— 必须说,
		// 而不是只在 stderr 留一行(用户不看 stderr,而这个词的后果是长期的)。
		out += "\n注意:仓库的 go.mod 不完整,构建前补跑过 go mod tidy,这个插件引入了仓库原本没声明的模块依赖。"
	}
	if res.Audit.Time != "" {
		out += "\n白名单登记:" + res.Audit.Time + " " + res.Audit.Source
	}
	if !res.Local {
		out += "\n提示:若工具没出现,等热重载或 /reload 指令面(插件进程由 host-bridge watch 撤销/重挂)"
	}
	return out, nil
}

// installArtifact 装一个下载来的产物(不构建、不需要 Go)。
func (h *Host) installArtifact(url, id, name, home string) (string, error) {
	if err := install.ValidateArtifactArgs(id, name); err != nil {
		return "", errString(err.Error())
	}
	ok, err := h.askInstall(install.ConfirmFacts{
		ID: id, Source: url,
		BuildCmd: "从 " + url + " 下载已构建好的产物(**不执行任何构建命令**;本机不需要 Go)",
	})
	if err != nil {
		return "", err
	}
	if !ok {
		return "已取消安装 " + id, nil
	}
	res, err := install.InstallArtifact(url, id, name, home)
	if err != nil {
		return "", errString(err.Error())
	}
	out := "已安装 " + res.ID + " → " + res.Dir
	out += "\n产物:" + res.Source + "(下载所得,**未执行任何构建命令**)"
	out += "\n注意:下载地址由提供它的人声明,gah 不验签名 ⇒ 装的那一刻没有独立校验;" +
		"装完的哈希已进白名单,**装完再换会被下一次加载挡住**。"
	if res.Audit.Time != "" {
		out += "\n白名单登记:" + res.Audit.Time + " " + res.Audit.Source
	}
	return out, nil
}

// externalPlugins 取外部插件控制面(可空:profile 未装 host-bridge 时为 nil)。
func (h *Host) externalPlugins() sdk.ExternalPlugins {
	var extp sdk.ExternalPlugins
	if err := h.c.Inject("ctx.extplugins", &extp); err != nil {
		return nil
	}
	return extp
}

// pluginLifecycle 停用/启用一个外部插件(批二)。
//
// 二次确认仍然要求:停用是用户**在决定本机常驻执行什么**,与安装同重。
// 回执必须把「停用 ≠ 卸载」说清 —— 否则用户会以为插件已经删掉,重启后它又出现。
func (h *Host) pluginLifecycle(name string, enable bool) (string, error) {
	extp := h.externalPlugins()
	if extp == nil {
		return "", errString("该 profile 未装配外部插件控制面(没有 host-bridge),无法启停外部插件")
	}
	verb := "停用"
	if enable {
		verb = "启用"
	}
	ok, err := h.askInstall(install.ConfirmFacts{
		ID: name, Source: h.pluginHome(), BuildCmd: verb + "外部插件(不删文件,不撤销白名单)",
	})
	if err != nil {
		return "", err
	}
	if !ok {
		return "已取消" + verb + " " + name, nil
	}
	if enable {
		if err := extp.Enable(name); err != nil {
			return "", errString(err.Error())
		}
		return "已启用 " + name + "(文件与白名单条目一直都在,不需要重新登记)", nil
	}
	if err := extp.Disable(name); err != nil {
		return "", errString(err.Error())
	}
	return "已停用 " + name + "(进程已停、工具已撤;**文件与白名单条目留着**,重新启用不需要重新登记;" +
		"要彻底删掉用 /install uninstall <id>)", nil
}

// guardInstallApproval 审批档门(strict 拒绝);未装配审批服务 ⇒ 放行(与 Web 面同口径)。
func (h *Host) guardInstallApproval() error {
	var ap sdk.ApprovalService
	if err := h.c.Inject("ctx.approval", &ap); err != nil || ap == nil {
		return nil
	}
	mode := ap.Mode()
	if es, ok := ap.(sdk.EffectiveApproval); ok {
		mode = es.EffectiveMode()
	}
	return install.GuardApproval(mode)
}

// askInstall 弹二次确认(无确认通道 ⇒ 拒绝,安全侧默认)。
func (h *Host) askInstall(f install.ConfirmFacts) (bool, error) {
	var cf sdk.ConfirmService
	if err := h.c.Inject("ctx.confirm", &cf); err != nil || cf == nil {
		return false, errString("无确认通道:拒绝安装(请在有确认通道的界面里操作,或用命令行 gah -install)")
	}
	return cf.Confirm(context.Background(), install.ConfirmPrompt(f))
}

// installCheck 检查已装插件的来源是否有更新(check-then-ask 的第一步)。
//
// 为什么不做自动更新:见 internal/install/updatecheck.go 头部三条理由。
// 要装新版就**重跑安装命令** —— 那时用户会重新看一遍确认文案并点确认。
func installCheck(home string) (string, error) {
	checks, err := install.CheckForUpdates(home)
	if err != nil {
		return "", errString(err.Error())
	}
	if len(checks) == 0 {
		return "(没有可检查的来源:本地目录、按 commit 固定的插件、以及下载来的产物都没有「更新」这回事)", nil
	}
	var b strings.Builder
	for _, c := range checks {
		target := c.Repo
		if c.Ref != "" {
			target += "@" + c.Ref
		}
		b.WriteString(strings.Join([]string{c.PluginID, c.Status, target, c.Message}, "\t") + "\n")
	}
	b.WriteString("\n注意:这里只检查、**不会自动装**。要装新版就重跑安装命令。\n")
	return strings.TrimRight(b.String(), "\n"), nil
}

// installRows 已安装插件清单(带白名单状态、最近登记来源与**装机时的 git 来源**)。
func installRows(home string) (string, error) {
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		return "", errString(err.Error())
	}
	items := install.List(home)
	if len(items) == 0 {
		return "(没有已安装的外部插件;用 /install <repo|目录> 安装)", nil
	}
	var b strings.Builder
	head := "ID\t协议\t二进制\t白名单"
	if list.Enforced() {
		head += "(强制)"
	} else {
		head += "(未启用)"
	}
	b.WriteString(head + "\t来源\n")
	// 来源账读不出来 ⇒ **整个清单报错**,不当空账。与 web 面同一口径:静默当成「这些插件
	// 没有来源记录」会让用户照着一个错误结论做安全判断(账坏了 ≠ 来源不明)。
	ledger, err := install.LoadSources(home)
	if err != nil {
		return "", errString(err.Error())
	}
	for _, it := range items {
		binary := install.BinaryName(it.Dir) // 手工放置的插件没 manifest,扫目录回落
		if binary == "" {
			binary = "(未找到二进制)"
		}
		state := "未登记"
		if a, ok := list.LastAuditOf(binary); ok {
			state = "已登记 " + a.Time[:16] + " " + a.Source
		}
		b.WriteString(strings.Join([]string{it.ID, it.Protocol, binary, state, sourceCell(ledger, it.ID)}, "\t") + "\n")
	}
	out := strings.TrimRight(b.String(), "\n")
	// 兼容性提示汇总在末尾(零网络;只看装机时记下的协议版本)。
	if notice := install.CompatibilityNoticeText(home); notice != "" {
		out += "\n\n注意:" + notice
	}
	return out, nil
}

// sourceCell 来源一格(装机的仓库/ref/sha)。
//
// 为什么要单独一格而不是塞进「白名单」那一列:白名单回答的是「盘上这份有没有被换」,
// 来源账回答的是「当初装的是哪一份」。两件事混在一格里,事后说不清是哪个变了。
func sourceCell(ledger *install.SourceLedger, id string) string {
	e, ok := ledger.Find(id)
	if !ok {
		return "(无来源记录:手工放置或早期安装)"
	}
	cell := e.Repo
	if e.Ref != "" {
		cell += "@" + e.Ref
	}
	cell += "(" + e.Kind
	if e.Commit != "" {
		cell += " · " + install.ShortSHA(e.Commit)
	}
	if e.Drifted {
		cell += " · 会移动"
	}
	return cell + ")"
}
