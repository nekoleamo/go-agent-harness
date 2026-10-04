// 构建子进程的**收口**(2026-10-03)。
//
// 动因:插件安装会在任何校验之前、在**宿主进程里**执行作者声明的构建命令。三件事因此
// 需要收紧 —— 都在这里做,是因为它们共享同一个入口(buildPlugin),散开改迟早会漏掉一处。
//
// ① 默认构建不再无条件 `go mod tidy`
//
// 原默认是 `go mod tidy && go build -o <bin> .`。`go mod tidy` 会**改写 go.mod/go.sum**,
// 而 go.mod 是作者可控的:一个只声明极少依赖(甚至空)的仓库可以在 tidy 阶段**新增**模块
// 需求、拉取新版本 —— 也就是「装一个插件」顺手把供应链面扩宽了。
// 现在改成:**先按 `-mod=readonly` 构建**(不改任何文件),失败了才跑一次 tidy 再重试,
// 并**如实回报"补过依赖"**。构建能力没减(不补就根本构建不出来),但"补依赖"从默认行为
// 变成了一个**被记录下来的事实**。
//
// ② 构建子进程的环境清洗
//
// 原先 `exec.Command` 不设 Env ⇒ 构建进程继承**宿主全部环境变量**,包括
// `*_API_KEY` / `*_TOKEN` / `AWS_*` / `GAH_CB_*`。插件的构建脚本(任意 shell)因此能直接
// 把它们读走 —— 这与"插件拿不到凭据"的整体口径矛盾(工具侧一直是 SanitizedEnv)。
// 同一处也把 `git clone` 的环境洗了(私有仓库的 SSH 凭据在 HOME/SSH_AUTH_SOCK,两者都保留)。
//
// ③ 已知遗留(不在本文件解决):**文件**层面的凭据仍可达。
// 环境清洗只挡住"从环境变量读",挡不住构建脚本去读 `$GAH_HOME/config/provider.yaml` ——
// 那需要把构建放进 `kernelsandbox`(macOS 可加凭据目录读拒),而那条路在 CI 上验不到
// (CI 跑 Linux,macOS 的 seatbelt 分支只有真机能验)。故如实登记,不在没有真机验证的
// 情况下改构建路径 —— 构建一旦被沙箱挡住,失败原因极难归集。
package install

import (
	"fmt"
	"os"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildEnvOverride 我们的默认构建要盖掉的宿主开关。
//
// 为什么连这些也要动:它们**改变构建语义**。宿主的 GOFLAGS=-mod=vendor 落到一次
// `-mod=readonly` 的构建上,结果可能完全不同(而用户完全不知道);GO111MODULE=off
// 会让构建根本不走模块。这些不是凭据,但同样属于"宿主的意外设置决定了这次安装的产物"。
var buildEnvOverride = []string{
	"GOFLAGS", "GO111MODULE", "GOWORK", "GOPRIVATE", "GONOSUMDB", "GONOSUMCHECK", "GOSUMDB",
}

// buildEnvOverrideAlways 无论如何都剥掉的那些(与显式放行口无关)。
//
// 为什么它们没有放行口:它们**改变构建语义**,而语义该由**这次安装**决定,不该由宿主的
// 意外设置决定。用户机器上恰好有 `GOFLAGS=-mod=vendor` 时,一次"只读构建"的结果会完全不同,
// 而他完全不知情;`GO111MODULE=off` 会让构建根本不走模块。这类不是凭据,但同样属于
// 「宿主的意外设置决定了这次安装的产物」。
var buildEnvOverrideAlways = []string{"GOFLAGS", "GO111MODULE", "GOWORK"}

// AllowPluginGoEnvEnv 放行口:把 `GOPRIVATE`/`GONOSUMDB`/`GOSUMDB` 也**不下发**给构建子进程
// (默认剥掉)。设为 `1` 时保留它们。
const AllowPluginGoEnvEnv = "GAH_ALLOW_PLUGIN_GOENV"

// buildEnvGoEnvAllowed 显式放行口是否打开(GAH_ALLOW_PLUGIN_GOENV=1)。
func buildEnvGoEnvAllowed() bool { return os.Getenv(AllowPluginGoEnvEnv) == "1" }

// buildEnv 构造构建子进程环境:清洗凭据 + 去掉上表里的宿主开关,再叠加我们自己的设定。
func buildEnv(extra ...string) []string {
	override := buildEnvOverrideAlways
	if !buildEnvGoEnvAllowed() {
		override = buildEnvOverride
	}
	base := sdk.SanitizedEnv(os.Environ())
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if containsFold(override, k) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, extra...)
}

// containsFold 大小写不敏感的成员判定(Windows 上环境变量名大小写不敏感)。
func containsFold(list []string, k string) bool {
	for _, v := range list {
		if strings.EqualFold(v, k) {
			return true
		}
	}
	return false
}

// buildResult 一次构建的结果。
type buildResult struct {
	// Cmd 实际执行的命令(可能不止一条:补依赖时会先跑一次 tidy)。回显给用户看。
	Cmd string
	// Tidied 是否执行过 `go mod tidy`。true ⇒ **这个插件引入了仓库里没声明的依赖**,
	// 属于「装插件顺带扩宽了供应链面」,必须让人看见。
	Tidied bool
}

// defaultBuildCmd 默认构建命令(不含 tidy;tidy 由 buildPlugin 在**需要时**补跑)。
//
// `-mod=readonly` 显式写出来而不是依赖 Go 的默认值:默认构建的语义是"本项目的选择",
// 不该取决于用户机器上装的是哪个 Go 版本。
func defaultBuildCmd(binary string) string {
	return fmt.Sprintf("GOFLAGS=-mod=readonly go build -o %s .", binary)
}

// buildPlugin 执行构建:先按 buildCmd 构建;**仅当**它失败时才跑一次 `go mod tidy` 并重试。
//
// 为什么是"失败才 tidy"而不是"永远不 tidy":go.mod 不完整时 readonly 构建必然失败,
// 而 tidy 是唯一的出路。区别在于 —— 旧默认无条件 tidy(即使构建根本不需要),新口径只在
// 真的需要时动 go.mod,并把"动过"这件事回报出去。
func buildPlugin(dir, buildCmd, binary string) (buildResult, error) {
	var res buildResult
	out, err := runEnv(dir, buildEnv(), "sh", "-c", buildCmd)
	if err == nil {
		res.Cmd = buildCmd
		return res, nil
	}
	// 作者自定义了 build:那不是我们的 Go 依赖问题,别插手(他的命令失败就是失败)。
	if strings.TrimSpace(cmdSource(dir)) != "" {
		res.Cmd = buildCmd
		return res, fmt.Errorf("install: 构建失败: %w(%s)", err, out)
	}
	// 默认路径:可能是 go.mod/go.sum 不完整。先 tidy 一次,再重试;两次都失败时报**第二次**
	// 的输出 —— 那是用户真正需要看的(第一次的"缺依赖"在补完之后已经没有意义了)。
	tidyCmd := "go mod tidy"
	if out2, terr := runEnv(dir, buildEnv(), "sh", "-c", tidyCmd); terr != nil {
		res.Cmd = buildCmd
		return res, fmt.Errorf("install: 补依赖失败(tidy): %w(%s)", terr, out2)
	}
	retry := defaultBuildCmd(binary)
	out3, err3 := runEnv(dir, buildEnv(), "sh", "-c", retry)
	if err3 != nil {
		res.Cmd = retry + "(已先执行 " + tidyCmd + ")"
		return res, fmt.Errorf("install: 构建失败(已尝试补依赖后): %w(%s)", err3, out3)
	}
	res.Cmd = retry + "(构建前补过一次依赖: " + tidyCmd + ")"
	res.Tidied = true
	return res, nil
}

// cmdSource 返回 plugin.yaml 里的 build 字段原文(空 = 走默认路径)。
func cmdSource(dir string) string {
	return strings.TrimSpace(readManifest(dir).Build)
}

// BuildEnvGoEnvNote 放行口的文案(进确认与文档)。
//
// 为什么需要这个口(批五 P4):默认剥掉 `GOPRIVATE`/`GONOSUMDB`/`GOSUMDB` 是对的 ——
// 剥掉 `GOPRIVATE` 会让 go 把**私有模块路径当公开模块**去查 sumdb,等于**泄漏模块路径**
// (企业内网的模块名/内部域名会出现在公网查询里)。但依赖私有模块的企业内部插件会构建失败,
// 而"构建不出来"对用户是死路。
//
// 显式开关的代价要说清:打开后 `GOSUMDB`/`GOPRIVATE` 会**原样进到作者的构建脚本**里 ——
// 不是凭据(那两个键里没有 token),但会改变 go 的模块解析与校验行为。
func BuildEnvGoEnvNote() string {
	if buildEnvGoEnvAllowed() {
		return "" // 已经打开,没什么要提醒的
	}
	return "构建子进程的 go 网络设置已被隔离(不下发 GOPRIVATE/GONOSUMDB/GOSUMDB)。" +
		"这能避免 go 把私有模块路径当公开模块去查 sumdb(那等于泄漏内部模块名)," +
		"但依赖私有模块的企业内部插件会构建失败 —— 那时用 " + AllowPluginGoEnvEnv + "=1 重跑。"
}

// AllowImplicitPluginBuildEnv 隐式构建的放行口(批六 §6.1 的 C 留口)。
//
// 现在恒为空实现(`implicitBuildAllowed()` 永远 false ⇒ C 永不触发,A 口径生效),
// 但**判定点与常量都在**:某天要改成「未声明 `build:` 一律拒绝」,只改这一个函数。
//
// 为什么留口而不直接删:A 口径要求三处标注「这条命令是默认的,不是你仓库里写的」——
// 而真要收紧时,那个收紧必须能对**长尾插件**说清为什么(它们绝大多数不写 `build:`)。
// 留一个显式开关比没有强:出问题时用户至少有一条可执行的路。
const AllowImplicitPluginBuildEnv = "GAH_ALLOW_IMPLICIT_PLUGIN_BUILD"

// implicitBuildAllowed C 口径是否已启用(= **拒绝**未声明 `build:` 的插件)。
//
// 返回 false ⇒ A 口径:照常装并标注。这是**当前**的默认,也是**唯一**在跑的形态。
// 语义方向别搞反:它是「要不要收紧」,不是「要不要放行」——
// 第一版写成 `!implicitBuildAllowed() && !AllowImplicitBuild` 导致默认反而拒绝,
// 全部既有安装用例当场变红。
func implicitBuildAllowed() bool { return false }
