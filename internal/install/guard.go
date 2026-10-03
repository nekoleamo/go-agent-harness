// 安装入口的审批门(2026-10-03):装插件 = 在本机引入一段**会常驻执行**的代码。
//
// 为什么要有这道门:与 `checkInstructionFaceWrite`(改「模型接下来要遵守的规则」要审批)
// 同一纪律 —— 能改规则的东西要审批,能改「本机常驻执行什么代码」的东西只会更重。
//
// 为什么不用 `tools/pre-execute` 走一遍裁决:那需要伪造一次工具调用(参数里塞一条假命令),
// 绕而不出;而这条门是**入口级**的判定,三个入口共用一份实现,比让每个入口各自记得问一遍
// 可靠得多。
//
// 口径:`strict` 档拒绝,`open`/`smart` 放行(二次确认仍是各入口的硬要求,这里不管)。
// 拒绝文案必须给出可执行的出路 —— 只说"不许"会让人以为功能坏了。
package install

import (
	"fmt"
	"path/filepath"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// GuardApproval 审批档门。mode = 当前**有效**审批档(已含审批联动与角色收紧,
// 由调用方取 sdk.EffectiveApproval;本函数不自己读服务,免得三处各取一次取成不同值)。
func GuardApproval(mode sdk.ApprovalMode) error {
	if mode != sdk.ApprovalStrict {
		return nil
	}
	return fmt.Errorf("install: 当前审批档是「严格」,拒绝安装插件。" +
		"装插件会在本机常驻一个进程并拿到与 gah 同级的沙箱围栏,属提权面动作。" +
		"请切到「智能」档(`/approval smart` 或设置面板)后重试,或先用命令行 gah -install")
}

// ConfirmFacts 确认文案要含的四件事实(§方案 2.1)。
//
// 为什么做成函数而不是让各入口自己拼:三个入口(TUI/Web/GUI)文案不一致时,
// 用户在某一个入口点确认时看到的就少了某一条 —— 而少的那条恰好是他在做决定时最需要的。
type ConfirmFacts struct {
	Source    string // repo URL 或本地绝对路径
	ID        string // 插件 id
	Dir       string // 落位目录
	BuildCmd  string // plugin.yaml 的 build 命令原文(可为空 = 用默认 go build)
	Uninstall bool   // 卸载时为 true(文案反过来)
}

// ConfirmPrompt 拼确认文案。
func ConfirmPrompt(f ConfirmFacts) string {
	if f.Uninstall {
		return fmt.Sprintf("卸载插件 %s?\n来源:%s\n落位:%s\n"+
			"卸载只删插件目录与它的白名单条目;不影响你已建的会话与配置。", f.ID, f.Source, f.Dir)
	}
	build := f.BuildCmd
	if build == "" {
		build = "GOFLAGS=-mod=readonly go build -o <binary> .(默认)"
	}
	return fmt.Sprintf("安装插件 %s?\n来源:%s\n落位:%s\n将执行的构建命令:%s\n"+
		"构建进程拿不到你环境里的 API key/令牌(已清洗);但它仍能读到磁盘上的文件。\n"+
		"若仓库的 go.mod 不完整,构建前会自动补跑一次 go mod tidy —— 那会让这个插件引入"+
		"仓库原本没声明的模块依赖,面板会标出来。\n"+
		"装完它会在本机常驻一个进程,并拿到与 gah 同级的沙箱围栏。只装你信任的插件。",
		f.ID, f.Source, f.Dir, build)
}

// Preview 安装**前**的事实(确认文案用)。来源是本地目录时能读出完整事实;
// 来源是远端 repo 时读不到(不为了填一句文案就先把仓库 clone 一遍 —— 那是网络与磁盘的
// 真实开销,而且对一个不可信的远端来说,clone 之前先别把它写进任何地方)。
//
// 远端来源的缺口如实登记:id 与构建命令要等拉下来才知道。因此确认文案里那两项会显式写
// 「由仓库的 plugin.yaml 决定」,并在**装完后**把实际执行的构建命令与登记哈希回显给用户。
func Preview(spec, home string) ConfirmFacts {
	f := ConfirmFacts{Source: spec}
	if !isLocalDir(spec) {
		f.ID = "(由仓库的 plugin.yaml 决定)"
		f.Dir = filepath.Join(home, "plugins", "(同上)")
		f.BuildCmd = "(由仓库的 plugin.yaml 决定;它可以是任意 shell 命令)"
		return f
	}
	src, err := filepath.Abs(spec)
	if err != nil {
		f.ID = "(本地路径无法解析)"
		return f
	}
	f.Source = src
	man := readManifest(src)
	f.ID = man.ID
	if f.ID == "" {
		f.ID = "(plugin.yaml 缺 id,安装时会拒绝)"
	}
	binary := man.Binary
	if binary == "" {
		binary = "tool-" + man.ID
	}
	f.Dir = filepath.Join(home, "plugins", man.ID)
	f.BuildCmd = man.Build
	if f.BuildCmd == "" {
		f.BuildCmd = defaultBuildCmd(binary) + "(默认;不改 go.mod/go.sum)"
	}
	return f
}
