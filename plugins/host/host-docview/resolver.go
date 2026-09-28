// 统一路径解析器(DOC_PREVIEW_PLAN §8「路径」):相对路径锚定当前会话 workspace 根,
// 绝对路径经 sdk.Sandbox 校验;realpath 归一后做前缀归属校验(防 symlink/`..` 逃逸);
// Web 端(strict)进一步收窄为 [workspace ∪ $GAH_HOME/attachments] 并叠加密钥 deny-list。
package hostdocview

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Resolver 文档路径解析器。sb 可为 nil(TUI/CLI 未装配沙箱时按 full-access 处理)。
type Resolver struct {
	sb       sdk.Sandbox
	gahHome  string
	extra    []string // 额外允许根(realpath 后比较),默认 $GAH_HOME/attachments
	homeDir  string   // ~ 展开用(仅用户显式输入的路径;不参与 gah 运行数据落盘)
	workRoot string   // 构造时的工作根快照;只作 workRootOf 的兜底(正常路径都现取)
}

// NewResolver 构造解析器。
func NewResolver(sb sdk.Sandbox, gahHome string) *Resolver {
	r := &Resolver{sb: sb, gahHome: gahHome}
	if gahHome != "" {
		r.extra = append(r.extra, filepath.Join(gahHome, "attachments"))
	}
	r.homeDir = sdk.UserHome() // 家目录候选并集里的首选(与 shell 的 `~` 同源,见 sdk.UserHomes)
	if sb != nil && sb.Root() != "" {
		r.workRoot = sb.Root()
	} else if cwd, err := os.Getwd(); err == nil {
		r.workRoot = cwd
	}
	return r
}

// workRootOf 当前工作根(**动态**)。
// 为何不能只在构造时取一次:切工作区只改沙箱 root(host-cwd-sessions 广播 → SetRoot)与
// 进程 cwd,而 resolver 是插件 Start 时构造的 —— 快照一次就等于把切换后的新工作区
// 整个判成「不在工作区/附件目录内」→ **打开工作区内的文件返回 403**(真机反馈)。
func (r *Resolver) workRootOf() string {
	if r.sb != nil {
		if root := r.sb.Root(); root != "" {
			return root
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return r.workRoot // 兜底:构造时快照(Getwd 失败极罕见)
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
		abs = filepath.Join(r.workRootOf(), abs)
	}
	abs = filepath.Clean(abs)

	real, err := r.statPath(abs)
	// 回退:相对路径在 workspace 下找不到时,再按附件根试一次。
	// 真机上模型确实把「<时间戳>/<名>」当路径传进 doc_open(docview: 文件不存在: 20260916-213605),
	// 而那串正是附件目录的「相对附件根」形式;`/attachments/<rel>` 则是前端给模型的标识写法。
	// 两种都兜住:模型少写一层根,不该以「文件不存在」收场。
	if err != nil && os.IsNotExist(err) {
		if alt, ok := r.fallback(p); ok {
			if rr, err2 := r.statPath(alt); err2 == nil {
				real, err = rr, nil
			}
		}
	}
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

// statPath EvalSymlinks + Clean(仅做存在性与可解析性检查;目录/文件语义由调用方判)。
// 回退候选也走这里:路径规范化与逃逸校验只有一套。
func (r *Resolver) statPath(abs string) (string, error) {
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(real); err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}

// fallback 把「相对附件根」与「/attachments/<rel>」两种写法映射回附件根下的绝对路径。
// 只处理相对形式:绝对路径必须是真路径,不能靠猜。
func (r *Resolver) fallback(orig string) (string, bool) {
	if len(r.extra) == 0 {
		return "", false
	}
	slash := filepath.ToSlash(orig)
	var rel string
	switch {
	case strings.HasPrefix(slash, "/attachments/"):
		rel = strings.TrimPrefix(slash, "/attachments/")
	case !filepath.IsAbs(orig):
		rel = slash
	default:
		return "", false
	}
	if rel == "" {
		return "", false
	}
	return filepath.Join(r.extra[0], filepath.FromSlash(rel)), true
}

// checkStrict Web 端:根集合收窄 + deny-list + $GAH_HOME/config 拒绝。
func (r *Resolver) checkStrict(real, orig string) error {
	if err := r.checkDeny(real); err != nil {
		return err
	}
	roots := make([]string, 0, 1+len(r.extra))
	if wr := r.realRoot(r.workRootOf()); wr != "" {
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
// Windows 上路径大小写不敏感(盘符与目录名大小写由模型/shell 拼出,不一定与
// workspace 配置一致),纯字符串比较会把合法路径判成「不在根内」→ 403。
func within(root, p string) bool {
	return withinFold(root, p, runtime.GOOS == "windows")
}

// withinFold 带显式大小写折叠开关(便于在非 Windows 上覆盖该分支)。
func withinFold(root, p string, foldCase bool) bool {
	if root == "" {
		return false
	}
	if foldCase {
		root, p = strings.ToLower(root), strings.ToLower(p)
	}
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}
