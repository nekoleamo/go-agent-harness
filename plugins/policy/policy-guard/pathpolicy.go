// 路径策略(沙箱加固):realpath 归一 + 整段归属校验 + 密钥 deny-list。
//
// 此前 sandbox.go 只用 filepath.Clean + strings.HasPrefix 做前缀判定,存在两类逃逸:
//   - symlink:`ln -s /etc/passwd ws/x` 后 `file_read ws/x` 的 Clean 路径仍在 workspace 内;
//   - 密钥直读:workspace-write/read-only 下 `file_read $GAH_HOME/config/provider.yaml`
//     或 `~/.ssh/id_rsa` 可把 API key / 私钥读进模型上下文(击穿 sdk/env.go 的凭据隔离设计)。
//
// 本文件的判定与 host-docview/resolver.go 保持同一口径(realpath + 整段匹配 + deny-list),
// 避免两个子系统各写一套。
package policyguard

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 凭据名单本体在 sdk(单一事实源 —— tool-shell 的内核层 profile 用**同一份**,见
// sdk/credentialpath.go);此处只做本地别名:判定顺序与错误措辞仍归本文件。
var (
	denyBase = sdk.CredentialBaseNames()
	denyGlob = sdk.CredentialGlobs()
	denyDir  = sdk.CredentialHomeDirs()
)

// denySuffix 凭据拒绝的统一尾注:说明这是**设计而非故障**,并让模型别再换工具重试。
// 真机事故(2026-09-22):模型想改 search.yaml,被拒后连试 file_write/shell/file_read/file_write
// 四种工具都撞同一墙 —— 只报“拒绝访问”会让它以为换个入口就行。
const denySuffix = "(凭据不进模型上下文:与沙箱档位无关,换工具重试也没用;需要改配置请让用户自己编辑)"

// credentialPrefixMin 通配符字面前缀的最小长度(A9):`id_*` → `id_`(3) 才判;
// 更短的前缀(`*`、`i*`)与名单的关系太弱,拒了就是误报。
const credentialPrefixMin = 3

// globLiteralPrefix 段内通配符之前的字面前缀:`id_*` → (`id_`,true);无通配符 → (整段,false)。
func globLiteralPrefix(seg string) (string, bool) {
	if i := strings.IndexAny(seg, "*?["); i >= 0 {
		return seg[:i], true
	}
	return seg, false
}

// credentialNameMatch 单个 basename/路径段的凭据名单判定(单一事实源:denyPath 与 shell 段判定共用)。
// 返回命中条目与是否命中。三条判据:
//
//	① 与基准名完全相等(`id_rsa`);
//	② 后缀 glob 匹配(`*.pem`);
//	③ **通配符字面前缀**命中名单项(A9:shell 里 `id_*` 的意图就是 `id_rsa` —— 此前这类段因
//	   “不含分隔符与点”被当成裸词跳过 ⇒ `$P/id_*` 漏判)。
func credentialNameMatch(name string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(name))
	if s == "" {
		return "", false
	}
	for _, d := range denyBase {
		if s == d {
			return d, true
		}
	}
	for _, g := range denyGlob {
		if ok, _ := filepath.Match(g, s); ok {
			return g, true
		}
	}
	if pre, isGlob := globLiteralPrefix(s); isGlob && len(pre) >= credentialPrefixMin {
		for _, d := range denyBase {
			if strings.HasPrefix(d, pre) {
				return pre + "* ~ " + d, true
			}
		}
		for _, g := range denyGlob {
			if strings.HasPrefix(g, pre) {
				return pre + "* ~ " + g, true
			}
			// 反向:`my.key*` 的前缀已带后缀 `.key` ⇒ 该 glob 必然匹配 `*.key` 类名单项
			if suf := strings.TrimPrefix(g, "*"); strings.HasSuffix(pre, suf) {
				return pre + "* ~ " + g, true
			}
		}
	}
	return "", false
}

// userHomes 用户家目录候选(HOME → USERPROFILE → os.UserHomeDir),归一去重、按优先级返回。
//
// 单一事实源(2026-09-27 跨平台复核):此前本包三处各取一家 —— approval.go 读 `$HOME`,
// pathpolicy/shellpaths 读 `os.UserHomeDir()`。两者在 Windows 上不是一个东西
// (os.UserHomeDir 看 %USERPROFILE%,不认 HOME;MSYS 的 `~` 看 HOME),于是同一台机器上
// “$HOME 下的密钥目录”与“`~/.zshrc`”判出两个家 ⇒ 一边拦一边漏。
//
// 取并集用于**拒绝**(宁多拦不漏);取首个用于**展开**(与用户 shell 的 `~` 同源)。
func userHomes() []string {
	var out []string
	seen := map[string]bool{}
	add := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" {
			return
		}
		h = filepath.Clean(h)
		if seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	add(os.Getenv("HOME"))
	add(os.Getenv("USERPROFILE"))
	if h, err := os.UserHomeDir(); err == nil {
		add(h)
	}
	return out
}

// userHome 首选家目录(展开 `~`/`$HOME` 用);无任何候选时返回空串。
func userHome() string {
	if hs := userHomes(); len(hs) > 0 {
		return hs[0]
	}
	return ""
}

// denyPath 密钥类路径判定(基准名 / 后缀 glob / 用户密钥目录 / $GAH_HOME/config)。
// 与沙箱档位无关:凭据永不进入模型可见面。
func denyPath(abs string) error {
	base := filepath.Base(abs)
	if hit, ok := credentialNameMatch(base); ok {
		return fmt.Errorf("sandbox: 拒绝访问凭据类文件 %s%s(命中 %s)", base, denySuffix, hit)
	}
	real := resolveRealPath(abs)
	for _, home := range userHomes() {
		for _, d := range denyDir {
			if pathWithin(filepath.Join(home, d), real) {
				return fmt.Errorf("sandbox: 拒绝访问用户密钥目录 %s%s", d, denySuffix)
			}
		}
	}
	if cfg := sdk.CredentialConfigDir(); cfg != "" && pathWithin(cfg, resolveRealPath(abs)) {
		return fmt.Errorf("sandbox: 拒绝访问数据根配置目录 config/%s", denySuffix)
	}
	return nil
}

// resolveRealPath 归一化路径:目标存在 → EvalSymlinks;不存在 → 归一最近存在的父目录 + 剩余片段
// (防"父目录是 symlink"绕过,写新文件时目标自身尚不存在)。
func resolveRealPath(p string) string {
	p = filepath.Clean(p)
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(real)
	}
	dir, rest := p, ""
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return p // 根都不可解析:退回 Clean 结果(归属校验仍会拒跨根)
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(filepath.Clean(real), rest)
		}
	}
}

// pathCaseFold 路径比较是否大小写不敏感(Windows 卷名/段名不区分大小写)。
// 变量而非常量:单测需在非 Windows 机器上覆盖该分支(沿用 kernel.go 的 sandboxExec 惯例)。
var pathCaseFold = runtime.GOOS == "windows"

// pathWithin 整段归属校验(realpath 归一;root 与 p 均先归一,防 symlink 逃逸与 /root2 误判;
// Windows 上大小写不敏感,否则 D:\Repo 与 d:\repo\sub 会被误判为“根之外”)。
func pathWithin(root, p string) bool {
	r := resolveRealPath(root)
	if r == "" {
		return false
	}
	t := resolveRealPath(p)
	if pathCaseFold {
		r, t = strings.ToLower(r), strings.ToLower(t)
	}
	if t == r {
		return true
	}
	return strings.HasPrefix(t, r+string(filepath.Separator))
}

// sandboxGahHome 数据根(读放行的额外根之一:附件/文档/缓存均在 $GAH_HOME 下)。
func sandboxGahHome() string {
	if h := os.Getenv("GAH_HOME"); h != "" {
		return filepath.Clean(h)
	}
	return ""
}
