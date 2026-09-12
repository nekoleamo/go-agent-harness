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
)

// denyBase 密钥类文件名 deny-list(对齐 host-docview,含 gah 自身配置文件)。
var denyBase = []string{
	".env", ".env.local", ".env.production", ".env.development",
	".netrc", ".npmrc", ".pgpass", ".git-credentials",
	"credentials", "credentials.json", "credential.json",
	"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
	"secring.gpg", "keychain.json",
	"provider.yaml", "search.yaml", // gah 运行配置:含第三方 API key
}

// denyGlob 后缀类 deny-list。
var denyGlob = []string{"*.pem", "*.key", "*.p12", "*.pfx", "*.keystore", "*.jks", "*.ppk", "*_rsa", "*_ed25519"}

// denyDir 目录前缀 deny-list(相对用户的 HOME 判定).
var denyDir = []string{".ssh", ".gnupg", ".aws", ".config/gcloud"}

// denyPath 密钥类路径判定(基准名 / 后缀 glob / 用户密钥目录 / $GAH_HOME/config)。
// 与沙箱档位无关:凭据永不进入模型可见面。
func denyPath(abs string) error {
	base := strings.ToLower(filepath.Base(abs))
	for _, d := range denyBase {
		if base == d {
			return fmt.Errorf("sandbox: 拒绝访问凭据类文件 %s", filepath.Base(abs))
		}
	}
	for _, g := range denyGlob {
		if ok, _ := filepath.Match(g, base); ok {
			return fmt.Errorf("sandbox: 拒绝访问凭据类文件 %s", filepath.Base(abs))
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		real := resolveRealPath(abs)
		for _, d := range denyDir {
			if pathWithin(filepath.Join(home, d), real) {
				return fmt.Errorf("sandbox: 拒绝访问用户密钥目录 %s", d)
			}
		}
	}
	if h := sandboxGahHome(); h != "" && pathWithin(filepath.Join(h, "config"), resolveRealPath(abs)) {
		return fmt.Errorf("sandbox: 拒绝访问数据根配置目录 config/")
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
