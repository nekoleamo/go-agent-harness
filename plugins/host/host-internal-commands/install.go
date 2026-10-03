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
		if err := install.Uninstall(args[1], home); err != nil {
			return "", errString(err.Error())
		}
		return "已卸载 " + args[1] + "(白名单条目一并撤销)", nil
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
	default:
		spec := strings.TrimSpace(args[0])
		facts := install.Preview(spec, home)
		ok, err := h.askInstall(facts)
		if err != nil {
			return "", err
		}
		if !ok {
			return "已取消安装 " + spec, nil
		}
		res, err := install.Install(spec, home)
		if err != nil {
			return "", errString(err.Error())
		}
		out := "已安装 " + res.ID + " → " + res.Dir
		if res.BuildCmd != "" {
			out += "\n构建命令:" + res.BuildCmd
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

// installRows 已安装插件清单(带白名单状态与最近登记来源)。
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
	b.WriteString(head + "\n")
	for _, it := range items {
		binary := install.BinaryName(it.Dir) // 手工放置的插件没 manifest,扫目录回落
		if binary == "" {
			binary = "(未找到二进制)"
		}
		state := "未登记"
		if a, ok := list.LastAuditOf(binary); ok {
			state = "已登记 " + a.Time[:16] + " " + a.Source
		}
		b.WriteString(strings.Join([]string{it.ID, it.Protocol, binary, state}, "\t") + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
