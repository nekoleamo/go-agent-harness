//go:build linux && (amd64 || arm64 || loong64 || mips64 || mips64le || ppc64 || ppc64le || riscv64 || s390x || sparc64)

// kernel_linux.go:Linux 内核级沙箱 = Landlock(内核 5.13+ / ABI v1)。
//
// 为什么必须自举 helper:Landlock 的限制对**本进程及其所有后代**生效且**不可撤销**
// (prctl(no_new_privs) + landlock_restrict_self 之后无法解除)。若在插件进程里直接施加,
// 插件自己会被永久锁住 —— 之后的档位切换、full-access、甚至 jail 维护全部失效。
// 因此把插件二进制**自己再 exec 一次**作 helper:helper 在 init()(main 之前)施加限制,
// 再 syscall.Exec 真正的 shell —— 限制只覆盖这一次命令的进程树,随进程退出而消失。
//
// 权限模型:只 handled **写类**权利(读与网络不设限),与协作层"只管写目标"的范围对齐;
// 白名单路径 = 数据根 jail(两档都有)+ workspace 根(仅 workspace-write)。
package toolshell

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
	// LANDLOCK_CREATE_RULESET_VERSION:landlock_create_ruleset(NULL, 0, 该值) 查询 ABI 版本。
	llCreateRulesetVersion = 1
	// LANDLOCK_RULE_PATH_BENEATH:landlock_add_rule 的 rule_type。
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

// llExecFlag 自举 helper 的 argv[1] 魔数:只有我们自己包装的命令行才会带它。
const llExecFlag = "--gah-landlock-exec"

// 需要放行的设备白名单。为什么必须有:handled 含 WRITE_FILE/TRUNCATE 后,写这些节点同样被拒 ——
// 而 cmd 2>/dev/null 是最常见的写法,不放行会让两档下大量命令异常失败。
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

// warnPartial 一次性告警:内核级沙箱已启用,但部分设备项未能纳入白名单(可能导致相关命令失败)。
// 与 warnUnavailable 区分开:这里沙箱**生效了**,只是白名单不全 —— 措辞不能让人以为没开。
func warnPartial(items []string) {
	if len(items) == 0 {
		return
	}
	warnPartialOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "gah tool-shell: 内核层未放行 %d 项 /dev 白名单(%s);涉及这些设备的命令(如 2>/dev/null、pty)可能失败。\n",
			len(items), strings.Join(items, ", "))
	})
}

// landlockRulesetAttr 对应 struct landlock_ruleset_attr 的 v1 形态(handled_access_fs)。
// 该结构体是"可扩展结构"(内核 copy_min_struct_from_user,min = sizeof(u64) = 8):
// 传 8 字节在 ABI 1..N 上都被接受,内核把其余字段清零 —— 故只传 v1 字段最稳。
type landlockRulesetAttr struct {
	handledAccessFs uint64
}

// landlockPathBeneathAttr 对应 struct landlock_path_beneath_attr{__u64 allowed_access; __s32 parent_fd;}
// C 侧 sizeof = 16(尾部 4 字节填充),Go 同序字段布局一致(u64 + int32 → 对齐后 16 字节)。
type landlockPathBeneathAttr struct {
	allowedAccess uint64
	parentFd      int32
}

func platformSupportNote() string {
	return "Linux 需内核 5.13+ 且启用 Landlock(ABI 探测失败即视为不可用)"
}

var (
	llABIOnce sync.Once
	llABI     int
)

// landlockABI 查询内核 Landlock ABI 版本(<1 = 不可用)。只探一次。
func landlockABI() int {
	llABIOnce.Do(func() {
		llABI = probeLandlockABI()
	})
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

// platformWrap:探测 ABI → 以自身为 helper 重新 exec(argv = [self, 魔数, 档位, 根, 原命令…])。
// 档位与根走 argv(不经环境变量):helper 参数显式可见、不会被用户命令的环境继承干扰。
func platformWrap(mode sdk.SandboxMode, root string) []string {
	if landlockABI() < 1 {
		warnUnavailable("内核不支持 Landlock(landlock_create_ruleset 探测失败)")
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		warnUnavailable("无法定位自身可执行文件(自举 helper 需要): " + err.Error())
		return nil
	}
	return []string{self, llExecFlag, string(mode), resolvePath(root)}
}

// init 自举 helper 入口。仅在 argv 带魔数时生效 —— 正常启动(插件进程 / 宿主 gah)不受影响。
// 任何失败都 os.Exit(126):绝不继续执行**未受约束**的命令(宁可失败,不静默放行)。
func init() {
	if len(os.Args) < 5 || os.Args[1] != llExecFlag {
		return
	}
	if err := landlockSelfAndExec(os.Args[2], os.Args[3], os.Args[4:]); err != nil {
		fmt.Fprintln(os.Stderr, "gah tool-shell: 内核级沙箱自举失败: "+err.Error())
	}
	os.Exit(126) // Exec 成功则不会返回;返回即失败
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
func landlockSelfAndExec(modeStr, root string, argv []string) error {
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

	allow := []string{jailRoot()}
	if mode == sdk.SandboxWorkspace {
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("workspace 档位缺少 workspace 根")
		}
		allow = append(allow, root)
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

	// 目录规则用完整写权利集;单文件规则只能用**对文件适用**的权利:
	// man 2 landlock_add_rule 的 ERRORS 明确 —— allowed_access 含仅适用于目录的权利
	// (MAKE_*/REMOVE_DIR/REFER 等)而 parent_fd 指向单个文件时返回 EINVAL。
	fileRights := uint64(llAccessFSWriteFile)
	if abi >= 3 {
		fileRights |= llAccessFSTruncate // TRUNCATE 对文件适用(ABI>=3 才有)
	}

	// 白名单根(jail / workspace)是硬边界:加不上就整体失败(失败即 os.Exit(126)),
	// 不许出现"白名单没加成功但命令照跑"= 静默放行。
	for _, dir := range allow {
		if err := llAddPathRule(rulesetFD, dir, handled); err != nil {
			return fmt.Errorf("landlock_add_rule(%s) 失败: %v", dir, err)
		}
	}

	var skipped []string
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
	warnPartial(skipped) // 白名单不全必须可见,但不阻断本次执行

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
