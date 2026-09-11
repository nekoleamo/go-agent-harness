// 统一路径解析器(DOC_PREVIEW_PLAN §8「路径」):相对路径锚定当前会话 workspace 根,
// 绝对路径经 sdk.Sandbox 校验;realpath 归一后做前缀归属校验(防 symlink/`..` 逃逸);
// Web 端(strict)进一步收窄为 [workspace ∪ $GAH_HOME/attachments] 并叠加密钥 deny-list。
package hostdocview

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Resolver 文档路径解析器。sb 可为 nil(TUI/CLI 未装配沙箱时按 full-access 处理)。
type Resolver struct {
	sb       sdk.Sandbox
	gahHome  string
	extra    []string // 额外允许根(realpath 后比较),默认 $GAH_HOME/attachments
	homeDir  string   // ~ 展开用(仅用户显式输入的路径;不参与 gah 运行数据落盘)
	workRoot string   // 无沙箱时的相对路径根(默认 cwd)
}

// NewResolver 构造解析器。
func NewResolver(sb sdk.Sandbox, gahHome string) *Resolver {
	r := &Resolver{sb: sb, gahHome: gahHome}
	if gahHome != "" {
		r.extra = append(r.extra, filepath.Join(gahHome, "attachments"))
	}
	r.homeDir, _ = os.UserHomeDir()
	if sb != nil && sb.Root() != "" {
		r.workRoot = sb.Root()
	} else if cwd, err := os.Getwd(); err == nil {
		r.workRoot = cwd
	}
	return r
}

// denyBase 密钥类文件名 deny-list(strict 模式生效;对齐全局规则「不读取含密钥的配置文件」)。
var denyBase = []string{
	".env", ".env.local", ".env.production", ".env.development",
	".netrc", ".npmrc", ".pgpass", ".git-credentials",
	"credentials", "credentials.json", "credential.json",
	"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
	"secring.gpg", "keychain.json",
}

// denyGlob 后缀类 deny-list(strict)。
var denyGlob = []string{"*.pem", "*.key", "*.p12", "*.pfx", "*.keystore", "*.jks", "*.ppk", "*_rsa", "*_ed25519"}

// Resolve 解析并校验路径(要求为文件),返回可用的绝对路径(realpath)。
// strict=true 时启用 Web 端更严策略(根集合收窄 + deny-list + $GAH_HOME/config 拒绝)。
func (r *Resolver) Resolve(p string, strict bool) (string, error) {
	return r.resolve(p, strict, false)
}

// ResolveDir 同 Resolve,但允许目标为目录(文件树/工作台用)。
func (r *Resolver) ResolveDir(p string, strict bool) (string, error) {
	return r.resolve(p, strict, true)
}

func (r *Resolver) resolve(p string, strict, allowDir bool) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("%w: 空路径", sdk.ErrDocDenied)
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: 路径含 NUL 字节", sdk.ErrDocDenied)
	}
	if strings.HasPrefix(p, "../") || p == ".." || strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") {
		// 显式拒绝(即便 Join 后仍在根内也不放行:相对路径不得跨目录)
		return "", fmt.Errorf("%w: 相对路径不得包含 ..(%s)", sdk.ErrDocDenied, p)
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if r.homeDir == "" {
			return "", fmt.Errorf("%w: 无法展开 ~(HOME 未知)", sdk.ErrDocDenied)
		}
		p = filepath.Join(r.homeDir, strings.TrimPrefix(p, "~/"))
	}

	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(r.workRoot, abs)
	}
	abs = filepath.Clean(abs)

	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", sdk.ErrDocNotFound, p)
		}
		return "", fmt.Errorf("%w: 无法解析路径 %s: %v", sdk.ErrDocDenied, p, err)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("%w: %s", sdk.ErrDocNotFound, p)
	}
	if fi.IsDir() && !allowDir {
		return "", fmt.Errorf("%w: 目标是目录(预览仅支持文件)", sdk.ErrDocDenied)
	}
	real = filepath.Clean(real)

	if strict {
		if err := r.checkStrict(real, p); err != nil {
			return "", err
		}
		return real, nil
	}
	if err := r.checkSandbox(real, p); err != nil {
		return "", err
	}
	return real, nil
}

// checkStrict Web 端:根集合收窄 + deny-list + $GAH_HOME/config 拒绝。
func (r *Resolver) checkStrict(real, orig string) error {
	if err := r.checkDeny(real); err != nil {
		return err
	}
	roots := make([]string, 0, 1+len(r.extra))
	if wr := r.realRoot(r.workRoot); wr != "" {
		roots = append(roots, wr)
	}
	for _, e := range r.extra {
		if er := r.realRoot(e); er != "" {
			roots = append(roots, er)
		}
	}
	for _, root := range roots {
		if within(root, real) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s 不在工作区/附件目录内(Web 端不允许任意绝对路径)", sdk.ErrDocDenied, orig)
}

// checkSandbox TUI/CLI:沿用沙箱三档语义(tool-files 同档)。
func (r *Resolver) checkSandbox(real, orig string) error {
	if r.sb == nil {
		return nil
	}
	switch r.sb.Mode() {
	case sdk.SandboxFullAccess, sdk.SandboxReadOnly:
		return nil // 读放行
	default: // workspace-write:读限 workspace 内(防读外泄)
		root := r.realRoot(r.sb.Root())
		if root == "" {
			return nil
		}
		if !within(root, real) {
			return fmt.Errorf("%w: workspace-write 拒绝访问 workspace 之外: %s", sdk.ErrDocDenied, orig)
		}
		return nil
	}
}

// checkDeny 密钥 deny-list(基准名 + 后缀 glob + $GAH_HOME/config 前缀)。
func (r *Resolver) checkDeny(real string) error {
	base := filepath.Base(real)
	lower := strings.ToLower(base)
	for _, d := range denyBase {
		if lower == d {
			return fmt.Errorf("%w: 拒绝访问密钥类文件 %s", sdk.ErrDocDenied, base)
		}
	}
	for _, g := range denyGlob {
		if ok, _ := filepath.Match(g, lower); ok {
			return fmt.Errorf("%w: 拒绝访问密钥类文件 %s", sdk.ErrDocDenied, base)
		}
	}
	if r.gahHome != "" {
		cfg := filepath.Join(r.gahHome, "config")
		if within(filepath.Clean(cfg), real) {
			return fmt.Errorf("%w: 拒绝访问数据根配置目录 config/", sdk.ErrDocDenied)
		}
	}
	return nil
}

// realRoot 归一化根目录(EvalSymlinks 失败时退化为 Clean;根可能尚未创建)。
func (r *Resolver) realRoot(root string) string {
	if root == "" {
		return ""
	}
	if rr, err := filepath.EvalSymlinks(root); err == nil {
		return filepath.Clean(rr)
	}
	return filepath.Clean(root)
}

// within 前缀归属校验(整段匹配,防 /root2 误判为 /root 内)。
func within(root, p string) bool {
	if root == "" {
		return false
	}
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}
