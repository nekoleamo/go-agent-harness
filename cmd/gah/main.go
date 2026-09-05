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
	"syscall"

	"github.com/nekoleamo/go-agent-harness/bundles"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/internal/embed"
	"github.com/nekoleamo/go-agent-harness/internal/install"
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
	installFlag = flag.String("install", "", "安装线上插件(M6.6):<repo>[@version] 或 mcp:<id>:<command>,装完即启用")
	uninstallFl = flag.String("uninstall", "", "卸载插件:<id>(删 home/plugins/<id>,桥 watch 自动撤销)")
	listPlugins = flag.Bool("list-plugins", false, "列出已安装的外部插件")
)

	// 非 TTY 检测(TUI profile):stdin 为 pipe/重定向时 bubbletea 会直读 stdin 卡死挂起;
	// 显式拒绝并提示走 headless(或提供 -input)——不再死机。
func main() {
	flag.Parse()
	if !isStdinTTY() && *inputFlag == "" && *profileFlag == "tui" {
		fmt.Fprintln(os.Stderr, "gah: TUI 需要交互式终端(stdin 非 TTY)。管道/后台场景请用: -profile headless -input <文本>")
		os.Exit(1)
	}

	if *showVersion {
		fmt.Printf("gah %s (github.com/nekoleamo/go-agent-harness)\n", version)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// 版本贯通(M7):mcp-server 等插件经 GAH_VERSION 读取构建版本
	os.Setenv("GAH_VERSION", version)

	// 1. 运行时 home 初始化:首启释放 seed 样板(home/config),ephemeral 用临时 home 退出即焚(设计 §7.3)
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
	// 插件安装/卸载/清单(M6.6):seed 释放后可写登记 patch 与 profile 引用
	if *installFlag != "" {
		res, err := install.Install(*installFlag, home)
		if err != nil {
			logger.Error("install: 失败", "err", err)
			os.Exit(1)
		}
		fmt.Printf("已安装 %s(protocol=%s)\n", res.ID, res.Protocol)
		if res.Binary != "" {
			fmt.Printf("  产物: %s%s\n", res.Binary, "(host-bridge watch 自动生效)")
		}
		fmt.Printf("  登记: %s(profile 已自动引用,下一轮启动即启用)\n", res.Patch)
		return
	}
	if *uninstallFl != "" {
		if err := install.Uninstall(*uninstallFl, home); err != nil {
			logger.Error("uninstall: 失败", "err", err)
			os.Exit(1)
		}
		fmt.Printf("已卸载 %s(工具经 host-bridge watch 自动撤销)\n", *uninstallFl)
		return
	}
	if *listPlugins {
		for _, it := range install.List(home) {
			protocol := it.Protocol
			if protocol == "" {
				protocol = "bridge"
			}
			fmt.Printf("%s\t%s	%s\n", it.ID, protocol, it.Binary)
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

	reg, c, err := bootWithRecovery(logger, tree, profilePath, bundleNames)
	if err != nil {
		logger.Error("boot: 启动失败(已尝试回滚):", "err", err)
		os.Exit(1)
	}
	defer reg.DisposeAll()
	// 启动成功 → 备份当前生效配置(下次坏配置可回滚)
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
func bootWithRecovery(logger *slog.Logger, tree *config.Tree, profilePath string, bundleNames []string) (*plugin.Registry, *ctx.Ctx, error) {
	reg, c, err := assembleAndStart(logger, tree, profilePath, bundleNames)
	if err == nil {
		return reg, c, nil
	}
	// 自愈:尝试回滚最近正常备份
	logger.Error("boot: 启动失败,尝试回滚上次正常配置", "err", err)
	backup, berr := config.LoadLatestBackup(profilePath)
	if berr != nil {
		return nil, nil, fmt.Errorf("%v(且回滚读取失败: %v)", err, berr)
	}
	if backup == nil {
		return nil, nil, fmt.Errorf("%v(无备份可回滚)", err)
	}
	logger.Warn("boot: 已回滚到上次正常配置,重试启动")
	reg2, c2, err2 := assembleAndStart(logger, backup, profilePath, bundleNames)
	return reg2, c2, err2
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
		out[id] = sdk.PluginInfo{ID: id, Type: d.Manifest.Type, Bundle: d.Bundle}
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

// homeDir 运行时主目录:$GAH_HOME 或 ~/.gah。
func homeDir() string {
	if h := os.Getenv("GAH_HOME"); h != "" {
		return h
	}
	if uh, err := os.UserHomeDir(); err == nil {
		return filepath.Join(uh, ".gah")
	}
	return os.TempDir()
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
