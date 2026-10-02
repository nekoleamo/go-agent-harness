//go:build linux && (amd64 || arm64 || loong64 || mips64 || mips64le || ppc64 || ppc64le || riscv64 || s390x || sparc64)

// linux.go:Linux 内核沙箱 = Landlock(内核 5.13+ / ABI v1)。
//
// 为什么必须自举 helper:Landlock 的限制对**本进程及其所有后代**生效且**不可撤销**
// (prctl(no_new_privs) + landlock_restrict_self 之后无法解除)。若在长期存活的进程里直接施加
// (宿主 gah / 插件进程本身),它自己会被永久锁住 —— 之后的档位切换、full-access、jail 维护全失效。
// 因此把**当前可执行文件自己再 exec 一次**作 helper:helper 在包 init()(main 之前)施加限制,
// 再 syscall.Exec 真正的目标程序 —— 限制只覆盖这一次命令的进程树,随进程退出而消失。
//
// 谁做 helper:`os.Executable()` = 被包装方所在的那个二进制(宿主 gah、外部插件、或任何
// 链接了本包的程序)。所以拦截点放在本包的 init() 而不是各调用方 —— 调用方漏调一次,
// 包装就会退化成"带着 --gah-landlock-exec 参数去跑正常逻辑",那种故障极难定位。
//
// 权限模型:只 handled **写类**权利(读与网络不设限),与协作层"只管写目标"的范围对齐;
// 白名单 = jail + workspace 根 + RW 额外项(包管理器缓存等)。
//
// 已知差异(诚实登记):macOS 侧另有**凭据目录读拒绝**(Spec.ReadDeny),本分支**没有**等价能力
// —— Landlock 规则是 additive allow-list,无法表达"除凭据目录外全放行读"
// (handled 含 READ_FILE 就必须逐层放行,漏一层即读不了)。
// 因此 Linux 上凭据读仍只有协作层文本判定(解释器内动态拼路径可绕过,见 A4)。
package kernelsandbox

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 常量来源:内核 include/uapi/linux/landlock.h(v5.13+)。x/sys v0.45.0 只带系统调用号
// (unix.SYS_LANDLOCK_*),不带这里的取值常量与结构体,故本地定义并写明来源。
const (
	// llCreateRulesetVersion:landlock_create_ruleset(NULL, 0, 该值) 查询 ABI 版本。
	llCreateRulesetVersion = 1
	// llRulePathBeneath:landlock_add_rule 的 rule_type。
	llRulePathBeneath = 1

	// handled/allowed 访问权(仅取写类):
	llAccessFSWriteFile  = 1 << 1  // WRITE_FILE
	llAccessFSRemoveDir  = 1 << 4  // REMOVE_DIR
	llAccessFSRemoveFile = 1 << 5  // REMOVE_FILE
	llAccessFSMakeChar   = 1 << 6  // MAKE_CHAR
	llAccessFSMakeDir    = 1 << 7  // MAKE_DIR
	llAccessFSMakeReg    = 1 << 8  // MAKE_REG
	llAccessFSMakeSock   = 1 << 9  // MAKE_SOCK
	llAccessFSMakeFifo   = 1 << 10 // MAKE_FIFO
	llAccessFSMakeBlock  = 1 << 11 // MAKE_BLOCK
	llAccessFSMakeSym    = 1 << 12 // MAKE_SYM
	llAccessFSRefer      = 1 << 13 // REFER(ABI>=2)
	llAccessFSTruncate   = 1 << 14 // TRUNCATE(ABI>=3)
)

// llExecFlag 自举 helper 的 argv[1] 魔数:只有本包包装过的命令行才会带它。
const llExecFlag = "--gah-landlock-exec"

// 需要放行的设备白名单。为什么必须有:handled 含 WRITE_FILE/TRUNCATE 后,写这些节点同样被拒 ——
// 而 `cmd 2>/dev/null` 是最常见的写法,不放行会让两档下大量命令异常失败。
var (
	// llDeviceDirs 目录级规则(用完整写权利集)。/dev/pts 只能整目录放行:pty slave 名
	// (/dev/pts/N)是运行期动态分配的,无法逐项枚举;该目录内节点属当前用户,放行风险可接受。
	llDeviceDirs = []string{"/dev/pts"}
	// llDeviceFiles 单个设备文件的规则。只能用**对文件适用**的权利集:man 2 landlock_add_rule
	// 的 ERRORS 明确 —— 若 allowed_access 含仅适用于目录的权利(MAKE_*/REMOVE_DIR/REFER 等)
	// 而 parent_fd 指向单个文件,返回 EINVAL。
	llDeviceFiles = []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom", "/dev/tty", "/dev/ptmx"}
	// llDeviceAliases 动态 fd 别名(/dev/stdout 等):目标可能是管道,而 Landlock 本就不约束管道,
	// 故尽力尝试、失败静默跳过(不制造每次都出现的噪音告警)。
	llDeviceAliases = []string{"/dev/stdout", "/dev/stderr"}
)

var warnPartialOnce sync.Once

// warnPartial 一次性告警:内核沙箱已启用,但部分白名单项未能纳入(相关命令可能失败)。
// 与"未生效"告警分开措辞:这里沙箱**生效了**,只是白名单不全。
func warnPartial(spec Spec, items []string) {
	if len(items) == 0 {
		return
	}
	warnPartialOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "gah %s: 内核层未放行 %d 项白名单(%s);涉及这些路径的写可能失败(用 %s 显式点名可放开)。\n",
			spec.label(), len(items), strings.Join(items, ", "), "GAH_EXT_RW_PATHS")
	})
}

func platformSupportNote() string {
	return "Linux 需内核 5.13+ 且启用 Landlock(ABI 探测失败即视为不可用)"
}

// ---------- 自举 helper 入口(包 init:调用方漏调也不会退化) ----------

// init 自举 helper 入口。仅在 argv 带魔数时生效 —— 正常启动(宿主 gah / 插件进程)不受影响。
// 任何失败都 os.Exit(126):绝不继续执行**未受约束**的命令(宁可失败,不静默放行)。
func init() {
	if len(os.Args) < 7 || os.Args[1] != llExecFlag {
		return
	}
	if err := landlockSelfAndExec(os.Args[2], os.Args[3], os.Args[4], splitRWArg(os.Args[5]), os.Args[6:]); err != nil {
		fmt.Fprintln(os.Stderr, "gah-kernelsandbox: 内核级沙箱自举失败: "+err.Error())
	}
	os.Exit(126) // Exec 成功则不会返回;返回即失败
}

// ---------- ABI 探测 ----------

type landlockRulesetAttr struct {
	handledAccessFs uint64
}

// landlockPathBeneathAttr 对应 struct landlock_path_beneath_attr{__u64 allowed_access; __s32 parent_fd;}
// C 侧 sizeof = 16(尾部 4 字节填充),Go 同序字段布局一致(u64 + int32 → 对齐后 16 字节)。
type landlockPathBeneathAttr struct {
	allowedAccess uint64
	parentFd      int32
}

var (
	llABIOnce sync.Once
	llABI     int
)

// landlockABI 查询内核 Landlock ABI 版本(<1 = 不可用)。只探一次。
func landlockABI() int {
	llABIOnce.Do(func() { llABI = probeLandlockABI() })
	return llABI
}

func probeLandlockABI() int {
	r, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, llCreateRulesetVersion)
	if errno != 0 {
		return -int(errno)
	}
	if int(r) < 1 {
		return -1
	}
	return int(r)
}

// platformAvailableReason 本平台能否施加(空串 = 能):Landlock ABI 探测。
// 与 platformWrap 的第一道门是同一次探测,故两处结论必然一致。
func platformAvailableReason(Spec) string {
	if landlockABI() < 1 {
		return "内核不支持 Landlock(landlock_create_ruleset 探测失败)"
	}
	return ""
}

// platformWrap:探测 ABI → 以自身为 helper 重新 exec
// (argv = [self, 魔数, 档位, 根, jail, RW 编码, 原命令…])。
// 档位/根/白名单走 argv(不经环境变量):helper 参数显式可见、不会被用户命令的环境继承干扰。
func platformWrap(spec Spec) []string {
	if landlockABI() < 1 {
		WarnUnavailable(spec, "内核不支持 Landlock(landlock_create_ruleset 探测失败)")
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		WarnUnavailable(spec, "无法定位自身可执行文件(自举 helper 需要): "+err.Error())
		return nil
	}
	// jail 根在调用方(父进程)解析并**随 argv 传给 helper**,不在 helper 内反推:
	// helper 是重新 exec 的同一二进制,而环境 jail 已把子进程 TMPDIR 改到 <jail>/tmp ——
	// 若 helper 用 sdk.Home() 反推 jail,GAH_HOME 为空时会得到 <jail>/tmp/jail(自指且不存在)
	// → landlock_add_rule ENOENT → 自举失败 → 所有命令 exit 126(CI 实证:GAH_HOME 未设必现)。
	return []string{
		self, llExecFlag, string(spec.Mode),
		ResolvePath(spec.Root), ResolvePath(spec.Jail), joinRWArg(spec.RW),
	}
}

// joinRWArg / splitRWArg:RW 白名单经**单个分号分隔**的 argv 槽传递(路径可含空格,故不用空格分隔;
// 含分号的路径属病态情形,按文档登记的限制跳过)。
func joinRWArg(paths []string) string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.ReplaceAll(p, "\n", " "))
		}
	}
	return strings.Join(out, "\n")
}

func splitRWArg(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// llAddPathRule 对路径加一条 PATH_BENEATH 规则(allowed 必须与该路径类型匹配:目录用完整集,
// 单文件只能用文件级权利,否则 EINVAL)。
func llAddPathRule(rulesetFD int, path string, allowed uint64) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	pba := landlockPathBeneathAttr{allowedAccess: allowed, parentFd: int32(fd)}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(rulesetFD),
		uintptr(llRulePathBeneath), uintptr(unsafe.Pointer(&pba)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// landlockSelfAndExec 施加 Landlock 后 exec 目标命令(argv[0] 经 PATH 解析)。
func landlockSelfAndExec(modeStr, root, jail string, rw []string, argv []string) error {
	mode := sdk.SandboxMode(modeStr)
	switch mode {
	case sdk.SandboxReadOnly, sdk.SandboxWorkspace:
	default:
		// 档位非法 = 参数被破坏:绝不退化成"无限制执行"
		return fmt.Errorf("档位非法 %q(仅接受 %s / %s)", modeStr, sdk.SandboxReadOnly, sdk.SandboxWorkspace)
	}
	abi := landlockABI()
	if abi < 1 {
		return fmt.Errorf("内核不支持 Landlock(探测返回 %d)", abi)
	}
	if strings.TrimSpace(jail) == "" {
		return fmt.Errorf("缺少 jail 白名单根参数(自举参数被破坏)")
	}

	// 硬边界白名单:jail 两档都有;workspace 根仅 workspace 档。
	// 这份清单与 WritablePaths(Spec{Root,Jail,RW})**同一集合**;这里仍走 argv 且分
	// hard/rw 两组,是因为两者失败语义不同(hard 加不上 → exit 126,不许静默放行;
	// rw 加不上 → 只记 skipped,不能为一个还没建的缓存目录废掉整条命令)。
	// 集合一致性由 TestWritablePathsMatchesLandlockScope 钉住。
	hard := []string{jail}
	if mode == sdk.SandboxWorkspace {
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("workspace 档位缺少 workspace 根")
		}
		hard = append(hard, root)
	}

	handled := uint64(llAccessFSWriteFile | llAccessFSRemoveDir | llAccessFSRemoveFile |
		llAccessFSMakeChar | llAccessFSMakeDir | llAccessFSMakeReg | llAccessFSMakeSock |
		llAccessFSMakeFifo | llAccessFSMakeBlock | llAccessFSMakeSym)
	if abi >= 2 {
		handled |= llAccessFSRefer
	}
	if abi >= 3 {
		handled |= llAccessFSTruncate
	}

	attr := landlockRulesetAttr{handledAccessFs: handled}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset 失败: %v", errno)
	}
	rulesetFD := int(fd)

	// 目录规则用完整写权利集;单文件规则只能用**对文件适用**的权利(见 llDeviceFiles 注释)。
	fileRights := uint64(llAccessFSWriteFile)
	if abi >= 3 {
		fileRights |= llAccessFSTruncate // TRUNCATE 对文件适用(ABI>=3 才有)
	}

	// 硬边界加不上就整体失败(os.Exit(126)):不许出现"白名单没加成功但命令照跑"= 静默放行。
	for _, dir := range hard {
		if err := llAddPathRule(rulesetFD, dir, handled); err != nil {
			return fmt.Errorf("landlock_add_rule(%s) 失败: %v", dir, err)
		}
	}

	var skipped []string
	// RW 额外白名单(缓存/临时区):不存在的路径尝试建出来(包管理器缓存目录首次使用前不存在),
	// 仍加不上只记入 skipped —— 不因一个缓存目录缺失就让整条命令无法执行。
	for _, p := range rw {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			_ = os.MkdirAll(p, 0o755)
		}
		if err := llAddPathRule(rulesetFD, p, handled); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s(%v)", p, err))
		}
	}
	// /dev/pts:pty 模式必需(slave 名动态,只能整目录放行)
	for _, dir := range llDeviceDirs {
		if err := llAddPathRule(rulesetFD, dir, handled); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s(%v)", dir, err))
		}
	}
	// 设备文件:2>/dev/null 这类写法必须能跑
	for _, f := range llDeviceFiles {
		err := llAddPathRule(rulesetFD, f, fileRights)
		if err == nil {
			continue
		}
		if errno, ok := err.(unix.Errno); ok && (errno == unix.ENOENT || errno == unix.ENXIO) {
			continue // 该环境没有这个设备节点(容器里常见),不是问题
		}
		// EINVAL 等:可能是权利集不被接受 —— 退化为只用 WRITE_FILE 再试一次
		if err2 := llAddPathRule(rulesetFD, f, llAccessFSWriteFile); err2 != nil {
			skipped = append(skipped, fmt.Sprintf("%s(%v)", f, err2))
		}
	}
	// 动态 fd 别名:目标是管道/pty 时规则本不适用(管道不受 Landlock 约束),静默跳过
	for _, f := range llDeviceAliases {
		_ = llAddPathRule(rulesetFD, f, fileRights)
	}
	warnPartial(Spec{}, skipped) // 白名单不全必须可见,但不阻断本次执行

	// no_new_privs 是 restrict_self 的前置条件(同时阻止 setuid 提权逃逸)。
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(NO_NEW_PRIVS) 失败: %v", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFD), 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self 失败: %v", errno)
	}
	// 限制已对进程生效,规则集 fd 不再需要(避免它漏进子进程)。
	unix.Close(rulesetFD)

	// 目标命令经 PATH 解析(syscall.Exec 不做 PATH 查找)。
	bin := argv[0]
	if !strings.ContainsRune(bin, os.PathSeparator) {
		p, err := exec.LookPath(bin)
		if err != nil {
			return err
		}
		bin = p
	}
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("命令 %s 不可执行: %v", bin, err)
	}
	return syscall.Exec(bin, argv, os.Environ())
}
