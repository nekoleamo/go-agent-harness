// Command gah 是 Go Agent Harness 的入口,仅做挂载编排(对齐设计 §2.1 boot):
// 解析 profile → 合并配置树 → 装配插件注册表 → 按拓扑序启动;headless 模式跑一轮。零业务逻辑。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/nekoleamo/go-agent-harness/bundles"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/internal/embed"
	"github.com/nekoleamo/go-agent-harness/internal/install"
	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/plugins/catalogue"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// version 由构建注入(goreleaser/-ldflags),见设计 §7.4。
var version = "dev"

var (
	profileFlag = flag.String("profile", "tui", "profile 名(tui | headless | dev)")
	inputFlag   = flag.String("input", "", "headless:一次输入,跑一轮后输出模型回复并退出")
	dumpConfig  = flag.Bool("dump-config", false, "输出合并后的配置树并退出")
	ephemeral   = flag.Bool("ephemeral", false, "一次性模式:配置落 temp,退出即焚")
	showVersion = flag.Bool("version", false, "输出版本信息并退出")
	installFlag = flag.String("install", "", "安装线上插件(M6.6):<repo>[@version] 或 mcp:<id>:<command>,装完即启用;"+
		"version 可为 tag/branch/40 位 commit sha(装回指定的那一版)")
	uninstallFl  = flag.String("uninstall", "", "卸载插件:<id>(删 home/plugins/<id>,桥 watch 自动撤销)")
	listPlugins  = flag.Bool("list-plugins", false, "列出已安装的外部插件")
	installUIFl  = flag.String("install-ui", "", "安装 UI 插件(M7.2):<repo>[@version] 或本地目录;v-html 扫描拒装")
	uninstallUIF = flag.String("uninstall-ui", "", "卸载 UI 插件:<id>(删 home/ui-plugins/<id>,重载页面即回默认)")
	listUIPlugs  = flag.Bool("list-ui-plugins", false, "列出已安装的 UI 插件")
	trustPlugin  = flag.String("trust-plugin", "", "把已放在 plugins/ 的二进制登记进哈希白名单:<名>(如 tool-basic);确认来源可信后再执行")
	untrustPl    = flag.String("untrust-plugin", "", "从哈希白名单移除:<名>")
	trustUIFl    = flag.String("trust-ui-plugin", "", "把已放在 ui-plugins/ 的 UI 插件登记进完整性闸:<id>(批四:UI 侧闸默认强制,手工放置的插件需在此放行)")
	untrustUIFl  = flag.String("untrust-ui-plugin", "", "从 UI 插件完整性闸移除:<id>")
	listTrust    = flag.Bool("list-trusted-plugins", false, "列出插件哈希白名单")
	acceptDrift  = flag.Bool("accept-drift", false, "配合 -install:接受同名 tag 指向了新 commit(默认拒绝)")
	artifactFl   = flag.String("install-artifact", "", "装一个**别人已经构建好**的插件产物(URL):"+
		"本机不需要 Go/node/任何工具链,也不执行任何构建命令。必须配 -id <插件id> -name <tool-xxx>")
	artifactID   = flag.String("id", "", "配合 -install-artifact:插件 id(= 落位目录名)")
	artifactName = flag.String("name", "", "配合 -install-artifact:产物文件名(须 tool- / cmd- 开头)")
	prebuiltFl   = flag.Bool("prebuilt", false, "配合 -install:下载作者发布的预编译产物,**不执行仓库里的构建脚本**"+
		"(必须有 plugin.yaml 的 prebuilt 段且含当前平台;找不到就报错,不回退源码构建)")
	checkUpdates = flag.Bool("check-plugin-updates", false, "检查已装插件的来源是否有更新(只发 git ls-remote,不安装)")
)

// 非 TTY 检测(TUI profile):stdin 为 pipe/重定向时 bubbletea 会直读 stdin 卡死挂起;
// 显式拒绝并提示走 headless(或提供 -input)——不再死机。
func main() {
	// M7 入口糖:gah web ≡ gah --profile web(Web UI 形态,浏览器访问 http://127.0.0.1:2233)
	if len(os.Args) > 1 && os.Args[1] == "web" {
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
		*profileFlag = "web"
	}
	// S-P2-3 入口糖:gah acp ≡ gah --profile acp(ACP agent 形态,stdio 供编辑器如 Zed 拉起)
	if len(os.Args) > 1 && os.Args[1] == "acp" {
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
		*profileFlag = "acp"
	}
	// 文档阅读 CLI(D1):`gah doc <path> …` 纯读命令,不装配插件(零副作用,直连 host-docview)
	if isDocSubcommand(os.Args) {
		os.Exit(runDocCmd(os.Args[2:]))
	}
	flag.Parse()
	// -version 在任何 stdin 环境下都要能打出来(CI/脚本里 stdin 常常不是 TTY):
	// 它既是**纯查询**又常在管道里用,先于 TUI 护栏判定,免得 `gah -version | cat` 报
	// "TUI 需要交互式终端" 这种驴唇不对马嘴的错。
	if *showVersion {
		fmt.Printf("gah %s (github.com/nekoleamo/go-agent-harness)\n", version)
		return
	}
	if !isStdinTTY() && *inputFlag == "" && *profileFlag == "tui" {
		fmt.Fprintln(os.Stderr, "gah: TUI 需要交互式终端(stdin 非 TTY)。管道/后台场景请用: -profile headless -input <文本>")
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// 版本贯通(M7):mcp-server 等插件经 GAH_VERSION 读取构建版本
	os.Setenv("GAH_VERSION", version)

	// 运行时 home 初始化:首启释放 seed 样板(home/config),ephemeral 用临时 home 退出即焚(设计 §7.3)
	home := homeDir()
	if *ephemeral {
		tmp, err := os.MkdirTemp("", "gah-ephemeral-")
		if err != nil {
			logger.Error("boot: 创建 ephemeral home 失败", "err", err)
			os.Exit(1)
		}
		home = tmp
		defer os.RemoveAll(home)
	}
	if home == "" {
		// 数据根唯一 = 二进制同级 gah-data/(2026-09-16 收紧:不再接受 GAH_HOME env / ~/.gah)
		logger.Error("boot: 无法确定运行时数据目录。请将 gah 与 gah-data/ 置于同目录(目录需可写),数据根仅支持 gah-data")
		os.Exit(1)
	}
	// 用户显式设置的 GAH_HOME env 不再作为输入源(数据根已锁定便携 gah-data):不一致时告警防误导
	if h := os.Getenv("GAH_HOME"); h != "" && h != home {
		logger.Warn("boot: 忽略 GAH_HOME env(数据根仅允许二进制同级 gah-data)", "GAH_HOME", h, "gah-data", home)
	}
	// P3 统一 home 事实源:经 GAH_HOME 贯通插件层(host-bridge 默认扫描目录等),
	// ephemeral 模式彻底隔离(外部插件目录一并入临时 home,退出即焚)。
	os.Setenv("GAH_HOME", home)
	if _, err := embed.EnsureSeed(home); err != nil {
		logger.Error("boot: 首启释放样板失败", "err", err)
		os.Exit(1)
	}
	// P1 方案 B:随包外部插件首启释放(home/plugins/,已有跳过)
	if _, err := embed.EnsurePlugins(home); err != nil {
		logger.Warn("boot: 释放外部插件失败(跳过,可后续 gah -install)", "err", err)
	}
	// 标出「随 gah 附带的官方件」(批四 P3 的分类展示)。
	// 取**全部**官方插件名而不是本次释放的:第二次启动时释放列表是空的,用后者会永远标不上。
	if official, err := embed.OfficialPluginNames(); err == nil {
		if err := install.MarkOfficial(home, official); err != nil {
			// 标不出来只影响面板的分组展示,不影响加载 —— 记 WARN 不阻断启动。
			logger.Warn("boot: 标注官方插件来源失败(面板里它们会与「你安装的」混在一组)", "err", err)
		}
	}
	// 预置角色首启释放(home/roles/<id>/,角色目录已存在 = 整体跳过:保护用户编辑)
	if _, err := embed.EnsureRoles(home); err != nil {
		logger.Warn("boot: 释放预置角色失败(跳过,不影响启动)", "err", err)
	}
	// 插件安装/卸载/清单(M6.6):seed 释放后可写登记 patch 与 profile 引用
	if *artifactFl != "" {
		// 产物安装:不构建 ⇒ 本机可以完全没有 Go / node / make。
		// 两个路径参数先校验(确认文案要用安全的事实,而不是未验证过的拼接结果)。
		if err := install.ValidateArtifactArgs(*artifactID, *artifactName); err != nil {
			logger.Error("install-artifact: 失败", "err", err)
			os.Exit(1)
		}
		dir := filepath.Join(home, "plugins", *artifactID)
		fmt.Println(install.ConfirmPromptArtifact(install.ArtifactFacts{
			URL: *artifactFl, ID: *artifactID, Name: *artifactName, Dir: dir,
		}))
		res, err := install.InstallArtifact(*artifactFl, *artifactID, *artifactName, home)
		if err != nil {
			logger.Error("install-artifact: 失败", "err", err)
			os.Exit(1)
		}
		fmt.Printf("\n已安装 %s → %s\n", res.ID, res.Dir)
		fmt.Printf("  产物: %s(下载所得,**未执行任何构建命令**;本机不需要 Go)\n", res.Source)
		fmt.Printf("  登记: %s(profile 已自动引用)\n", res.Patch)
		return
	}
	if *installFlag != "" {
		res, err := install.InstallWithOpts(*installFlag, home,
			install.InstallOpts{AcceptDrift: *acceptDrift, Prebuilt: *prebuiltFl})
		if err != nil {
			logger.Error("install: 失败", "err", err)
			os.Exit(1)
		}
		fmt.Printf("已安装 %s(protocol=%s)\n", res.ID, res.Protocol)
		if res.Binary != "" {
			fmt.Printf("  产物: %s%s\n", res.Binary, "(host-bridge watch 自动生效)")
		}
		// 来源三件(仓库/ref/commit)必须回显:「我装的是哪一份代码」要当场看得见,
		// 而不是只能事后去翻 plugins/sources.yaml。
		if res.Record.Repo != "" {
			fmt.Printf("  来源: %s", res.Record.Repo)
			if res.Record.Ref != "" {
				fmt.Printf(" @%s", res.Record.Ref)
			}
			fmt.Printf("(%s", res.Record.Kind)
			if res.Record.Commit != "" {
				fmt.Printf(" · %s", install.ShortSHA(res.Record.Commit))
			}
			fmt.Printf(")\n")
		}
		if res.Prebuilt != "" {
			fmt.Printf("  产物: %s(作者发布的预编译产物,**未执行任何构建命令**)\n", res.Prebuilt)
		}
		if res.BuildImplicit {
			fmt.Printf("  注意:该仓库的 plugin.yaml **没有声明 build:** —— %s\n"+
				"        是 gah 替你选的默认命令,不是作者写的(装之前值得确认这个仓库确实是 Go 项目)\n", res.BuildCmd)
		}
		if w := res.DriftWarning(); w != "" {
			fmt.Println("  " + w)
		}
		fmt.Printf("  登记: %s(profile 已自动引用,下一轮启动即启用)\n", res.Patch)
		return
	}
	if *uninstallFl != "" {
		// ctl 传 nil:CLI 场景下 gah 进程本身没在跑(插件装了但服务没起),**没有活进程可停** ——
		// 而「停不掉就不删」那条规则正是为有活进程的场景写的,这里天然无事可做。
		if err := install.Uninstall(*uninstallFl, home, nil); err != nil {
			logger.Error("uninstall: 失败", "err", err)
			os.Exit(1)
		}
		fmt.Printf("已卸载 %s(白名单与来源账条目一并撤销;若另有一个 gah 服务正在跑,那侧的插件进程需自行停)\n", *uninstallFl)
		return
	}
	if *listPlugins {
		ledger, _ := install.LoadSources(home)
		for _, it := range install.List(home) {
			protocol := it.Protocol
			if protocol == "" {
				protocol = "bridge"
			}
			line := it.ID + "\t" + protocol + "\t" + install.ListBinaryName(it)
			if e, ok := ledger.Find(it.ID); ok {
				line += "\t" + e.Repo
				if e.Ref != "" {
					line += "@" + e.Ref
				}
				line += "(" + e.Kind
				if e.Commit != "" {
					line += " · " + install.ShortSHA(e.Commit)
				}
				if e.Drifted {
					line += " · 会移动"
				}
				line += ")"
			}
			fmt.Println(line)
		}
		if notice := install.CompatibilityNoticeText(home); notice != "" {
			fmt.Println("\n注意:" + notice)
		}
		return
	}
	// 检查更新(批一 §1.5):**只问不装**。网络只在这里发生一次(git ls-remote,不 clone)。
	if *checkUpdates {
		checks, err := install.CheckForUpdates(home)
		if err != nil {
			logger.Error("check-plugin-updates: 失败", "err", err)
			os.Exit(1)
		}
		if len(checks) == 0 {
			fmt.Println("(没有可检查的来源:本地目录、按 commit 固定的插件、以及下载来的产物都没有「更新」这回事)")
			return
		}
		for _, c := range checks {
			fmt.Printf("%s\t%s\t%s\t%s\n", c.PluginID, c.Repo, c.Status, c.Message)
		}
		return
	}
	// UI 插件安装/卸载/清单(M7.2):产物落 home/ui-plugins/<id>/,装配即生效(重载页面)
	if *installUIFl != "" {
		res, err := install.InstallUI(*installUIFl, home)
		if err != nil {
			logger.Error("install-ui: 失败", "err", err)
			os.Exit(1)
		}
		fmt.Printf("已安装 UI 插件 %s v%s(覆盖 %d 槽位)\n", res.ID, res.Version, res.Slots)
		fmt.Printf("  落位: %s\n", res.Dir)
		fmt.Printf("  生效: 重载 web 页面即换(ui-web-app 聚合自动发现)\n")
		return
	}
	if *trustPlugin != "" {
		if err := install.Trust(*trustPlugin, home); err != nil {
			logger.Error("trust-plugin: 失败", "err", err)
			os.Exit(1)
		}
		fmt.Printf("已登记 %s 到 plugins/%s(下次调用即生效,热重载路径同样校验)\n", *trustPlugin, plugintrust.FileName)
		return
	}
	// UI 插件放行口(批四):UI 侧无官方插件 ⇒ 闸由 boot 无条件创建 ⇒ 手工放进
	// ui-plugins/ 的插件默认被拒。这是本项目最宽的面上一道**默认**闸 + 一条明确出路。
	if *trustUIFl != "" {
		uiRoot := filepath.Join(home, "ui-plugins")
		slots, err := install.UIPluginSlots(uiRoot, *trustUIFl)
		if err != nil {
			logger.Error("trust-ui-plugin: 失败", "err", err)
			os.Exit(1)
		}
		if err := install.TrustUI(uiRoot, *trustUIFl, slots); err != nil {
			logger.Error("trust-ui-plugin: 失败", "err", err)
			os.Exit(1)
		}
		fmt.Printf("已登记 UI 插件 %s 到 ui-plugins/%s(下次打开设置面板即生效;只登记你看过的那一份)\n", *trustUIFl, plugintrust.FileName)
		return
	}
	if *untrustUIFl != "" {
		if err := install.UIUntrust(filepath.Join(home, "ui-plugins"), *untrustUIFl); err != nil {
			logger.Error("untrust-ui-plugin: 失败", "err", err)
			os.Exit(1)
		}
		fmt.Printf("已从 ui-plugins/%s 移除 %s\n", plugintrust.FileName, *untrustUIFl)
		return
	}
	if *untrustPl != "" {
		if err := install.Untrust(*untrustPl, home); err != nil {
			logger.Error("untrust-plugin: 失败", "err", err)
			os.Exit(1)
		}
		fmt.Printf("已从 plugins/%s 移除 %s\n", plugintrust.FileName, *untrustPl)
		return
	}
	if *listTrust {
		list, err := install.TrustedList(home)
		if err != nil {
			logger.Error("list-trusted-plugins: 失败", "err", err)
			os.Exit(1)
		}
		if len(list) == 0 {
			fmt.Println("(白名单为空或未启用)")
			return
		}
		for _, n := range list {
			fmt.Println(n)
		}
		return
	}
	if *uninstallUIF != "" {
		if err := install.UninstallUI(*uninstallUIF, home); err != nil {
			logger.Error("uninstall-ui: 失败", "err", err)
			os.Exit(1)
		}
		fmt.Printf("已卸载 UI 插件 %s(重载 web 页面回默认实现)\n", *uninstallUIF)
		return
	}
	if *listUIPlugs {
		for _, it := range install.ListUI(home) {
			slots := make([]string, 0, len(it.Slots))
			for _, s := range it.Slots {
				slots = append(slots, s.Name)
			}
			fmt.Printf("%s\tv%s\t[%s]\t%s\n", it.ID, it.Version, strings.Join(slots, ","), it.Dir)
		}
		return
	}
	profilePath := filepath.Join(home, "config")
	tree, err := loadProfileTree(profilePath, *profileFlag)
	if err != nil {
		logger.Error("boot: 加载 profile 失败", "err", err)
		os.Exit(1)
	}

	// 2. --dump-config:输出合并树即可退出(任一条目可被自己的 patch 替换)
	if *dumpConfig {
		raw, err := tree.DumpYAML()
		if err != nil {
			logger.Error("boot: dump-config 失败", "err", err)
			os.Exit(1)
		}
		fmt.Print(string(raw))
		return
	}

	// 3. 装配并启动插件(配置自愈:坏配置导致失败时回滚最近备份重试一次,再失败才退出)
	bundleNames, err := config.BundlesOfProfile(filepath.Join(profilePath, "profile-"+*profileFlag+".yaml"))
	if err != nil {
		logger.Error("boot: 读取 profile bundle 列表失败", "err", err)
		os.Exit(1)
	}

	reg, c, tree, err := bootWithRecovery(logger, tree, profilePath, bundleNames)
	if err != nil {
		logger.Error("boot: 启动失败(已尝试回滚):", "err", err)
		os.Exit(1)
	}
	defer reg.DisposeAll()
	// 启动成功 → 备份**实际生效**的配置(自愈回滚后是备份树,不是启动失败的那棵;
	// 若存的是失败树,第一次自愈就把唯一好备份换成坏配置 → 下次回滚必然失败,
	// 自愈能力退化为一次性)。
	if err := config.SaveBackup(tree, profilePath, 10); err != nil {
		logger.Warn("boot: 配置备份失败", "err", err)
	}

	logger.Info("gah booted",
		"profile", *profileFlag,
		"instances", reg.Snapshot(),
		"services", c.ListServiceKeys(),
	)

	// 4. headless 模式:跑一轮并输出模型回复;否则等待退出信号
	if *inputFlag != "" {
		if err := runHeadless(c, *inputFlag, logger); err != nil {
			logger.Error("headless: 运行失败", "err", err)
			os.Exit(1)
		}
		return
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	// TUI 退出 → system/shutdown 事件 → 应用退出(交互模式)
	shutdown := make(chan struct{}, 1)
	c.Subscribe("system/shutdown", func(ctx context.Context, ev *sdk.Event) error {
		select {
		case shutdown <- struct{}{}:
		default:
		}
		return nil
	})
	select {
	case <-sig:
	case <-shutdown:
	}
	logger.Info("gah shutting down")
}

// runHeadless 注入 agentLoop 跑一轮,输出最后一个 assistant 回复。
func runHeadless(c *ctx.Ctx, input string, logger *slog.Logger) error {
	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		return fmt.Errorf("headless: ctx.agentLoop 未装配: %w", err)
	}
	runCtx := context.Background()
	if err := loop.Run(runCtx, input); err != nil {
		return err
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		return err
	}
	// 输出最后一个 assistant 消息内容(UI 侧 UI 渲染;headless 输出纯文本)
	msgs := sessions.DeriveMessages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == sdk.RoleAssistant {
			fmt.Println(msgs[i].Content)
			break
		}
	}
	return nil
}

// bootWithRecovery 装配并启动插件;失败时回滚最近备份配置树重试一次。
func bootWithRecovery(logger *slog.Logger, tree *config.Tree, profilePath string, bundleNames []string) (*plugin.Registry, *ctx.Ctx, *config.Tree, error) {
	reg, c, err := assembleAndStart(logger, tree, profilePath, bundleNames)
	if err == nil {
		return reg, c, tree, nil
	}
	// 自愈:尝试回滚最近正常备份
	logger.Error("boot: 启动失败,尝试回滚上次正常配置", "err", err)
	backup, berr := config.LoadLatestBackup(profilePath)
	if berr != nil {
		return nil, nil, nil, fmt.Errorf("%v(且回滚读取失败: %v)", err, berr)
	}
	if backup == nil {
		return nil, nil, nil, fmt.Errorf("%v(无备份可回滚)", err)
	}
	logger.Warn("boot: 已回滚到上次正常配置,重试启动")
	reg2, c2, err2 := assembleAndStart(logger, backup, profilePath, bundleNames)
	if err2 != nil {
		return nil, nil, nil, err2
	}
	// 回传实际生效的树:调用方据此写备份,避免把失败配置存成新回滚点。
	return reg2, c2, backup, nil
}

// assembleAndStart 装配+启动(内部服务注入)。
func assembleAndStart(logger *slog.Logger, tree *config.Tree, profilePath string, bundleNames []string) (*plugin.Registry, *ctx.Ctx, error) {
	reg := plugin.New()
	bus := event.New(logger)
	c := ctx.New(logger, bus)

	// 内部服务:插件管理器/外部桥经 system.registry 访问注册表(仅暴露 sdk.RegistryOps 子集),
	// system.catalogue 提供候选插件清单(避免插件间 import 环)
	if err := c.Provide("system.registry", reg); err != nil {
		return nil, nil, err
	}
	if err := c.Provide("system.catalogue", catalogueInfo()); err != nil {
		return nil, nil, err
	}
	for _, name := range bundleNames {
		regFn, ok := bundles.Registry[name]
		if !ok {
			return nil, nil, fmt.Errorf("未知 bundle %q(配置声明了不存在的 bundle → 显式失败)", name)
		}
		if err := regFn(reg, tree); err != nil {
			return nil, nil, fmt.Errorf("装配 bundle %q: %v", name, err)
		}
	}
	if err := reg.StartSubset(c, enabledSet(tree)); err != nil {
		return nil, nil, err
	}
	return reg, c, nil
}

// catalogueInfo 构造候选插件清单一览(boot 单次,插件间无 import 环)。
func catalogueInfo() map[string]sdk.PluginInfo {
	out := make(map[string]sdk.PluginInfo)
	for id, d := range catalogue.All {
		out[id] = sdk.PluginInfo{ID: id, Type: d.Manifest.Type, Bundle: d.Bundle, Manage: d.Manage}
	}
	return out
}

// enabledSet 配置树中启用且已实现的条目集合(未实现条目不启动,留作 future 占位)。
func enabledSet(tree *config.Tree) map[string]bool {
	set := make(map[string]bool)
	for _, id := range tree.List() {
		if tree.Enabled(id) {
			set[id] = true
		}
	}
	return set
}

// isStdinTTY stdin 是否交互终端(char device);pipe/重定向 → false。
func isStdinTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// homeDir 运行时数据根(**唯一:与 gah 二进制同级的 gah-data/ 便携根**)。
//   - 已存在 → 用之;不存在 → 自动新建(初始化内容由首启 EnsureSeed/EnsurePlugins 释放)
//   - 创建失败(二进制目录只读/不可写)= 不可便携 → 返回空,由调用方报错退出
//   - GAH_HOME env 与 ~/.gah 均**不再作为输入源**(2026-09-16 收紧:数据根只允许 gah-data);
//     内部贯通仍经 boot 后 os.Setenv("GAH_HOME", home)(插件/外部进程读该 env 派生子目录)
//
// 任一分支都不落系统根(防根);全部运行数据统一在此单根下。
func homeDir() string {
	exe, err := os.Executable()
	if err == nil {
		exe = execRealPath(exe) // 符号链接归一:数据根跟随真实二进制(PATH/symlink 启动时一致)
		if pd := portableRoot(exe); pd != "" {
			return pd
		}
	}
	return "" // 不可便携:调用方报错退出
}

// execRealPath 归一符号链接(PATH 里 ln -s 启动时,os.Executable 返回入口链接路径;
// 数据根应随真实二进制目录,不随调用入口目录)。
func execRealPath(exe string) string {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real
	}
	return exe
}

// portableRoot 便携数据根解析:给定 gah 二进制路径(已由调用方 execRealPath 归一符号链接)
// → 同目录 gah-data/。已存在 → 用之;不存在 → MkdirAll 新建(成功后由首启释放填充
// 内容);创建失败(二进制目录只读/不可写)= 不可便携 → 返回空(调用方报错退出)。
func portableRoot(exe string) string {
	pd := filepath.Join(filepath.Dir(filepath.Clean(exe)), "gah-data")
	if fi, serr := os.Stat(pd); serr == nil && fi.IsDir() {
		return pd
	} else if serr != nil && os.IsNotExist(serr) {
		if merr := os.MkdirAll(pd, 0o755); merr == nil {
			return pd // 新建成功:homeDir 后续 EnsureSeed/EnsurePlugins 释放初始化内容
		}
	}
	return ""
}

// loadProfileTree 解析 profile 文件;bundle 由配置文件同目录解析。
// (M1:读取仓库 config/ 下的样板;M2 起改为 embed 释放到 home,见设计 §7.3)
func loadProfileTree(dir, name string) (*config.Tree, error) {
	resolve := func(bundleName string) ([]config.Entry, error) {
		b, err := config.ReadBundle(filepath.Join(dir, "bundle-"+bundleName+".yaml"))
		if err != nil {
			return nil, err
		}
		return b.Entries, nil
	}
	return config.LoadProfile(filepath.Join(dir, "profile-"+name+".yaml"), resolve)
}
