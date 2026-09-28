// credentialpath.go:凭据类路径规则的**单一事实源**。
//
// 为什么放 sdk 而不是各插件各写一份:同一份名单被两套机制使用,必须**逐字一致** ——
//   - policy-guard(协作层):按命令文本/工具参数判定,负责给出可读的拒绝理由(见 pathpolicy.go);
//   - tool-shell(内核层):把它写进内核 profile,拦**文本层看不见**的动态读 ——
//     2026-09-27 审计实测:`python3 -c "open('~/.ss'+'h/id_'+'rsa').read()"` 完全绕过文本层
//     (字面路径不出现 → denyPath 无从命中),而写侧早有内核层兜底、读侧没有。
//
// 两处若各存一份,迟早出现"协作层拒、内核层放"的空档。
//
// 注意本文件只提供**规则数据**:判定顺序、错误措辞仍由消费方决定(内核层无法给出理由文本,
// 只能拒绝)。
package sdk

import (
	"os"
	"path/filepath"
	"strings"
)

// CredentialBaseNames 密钥类文件名 deny-list(按 basename 判定)。
func CredentialBaseNames() []string {
	return []string{
		".env", ".env.local", ".env.production", ".env.development",
		".netrc", ".npmrc", ".pgpass", ".git-credentials",
		"credentials", "credentials.json", "credential.json",
		"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
		"secring.gpg", "keychain.json",
		"provider.yaml", "search.yaml", // gah 运行配置:含第三方 API key
	}
}

// CredentialGlobs 后缀类 deny-list(filepath.Match 语义,按 basename 判定)。
func CredentialGlobs() []string {
	return []string{"*.pem", "*.key", "*.p12", "*.pfx", "*.keystore", "*.jks", "*.ppk", "*_rsa", "*_ed25519"}
}

// CredentialHomeDirs 密钥目录 deny-list(相对**家目录候选**判定,见 UserHomes;元素可含多级路径)。
//
// 内核层读拒绝只覆盖本目录集(不含 CredentialBaseNames/Globs):`.npmrc`/`.netrc` 之类的
// **文件**是 npm/git/curl 等常规工具自己会读的,内核层一拒就是大面积工作流失败;目录
// (.ssh/.aws/.gnupg/gcloud)才是"被解释器读出来"的高价值目标。
func CredentialHomeDirs() []string { return []string{".ssh", ".gnupg", ".aws", ".config/gcloud"} }

// CredentialConfigDir 数据根配置目录($GAH_HOME/config,含 provider.yaml/search.yaml 等第三方 API key)。
// 未设 GAH_HOME(嵌入/单测)时返回空串 —— 不回退 sdk.Home()(那是 TempDir,会把临时目录当数据根)。
func CredentialConfigDir() string {
	h := os.Getenv("GAH_HOME")
	if h == "" {
		return ""
	}
	return filepath.Join(filepath.Clean(h), "config")
}

// CredentialDenyDirs 内核层读拒绝的目录**绝对路径**列表(每个家目录候选下的密钥目录 + $GAH_HOME/config)。
//
// 家目录取**并集**而非单家(2026-09-27 第二轮审计):只认 `$HOME` 时,Windows 桌面壳启动的
// gah.exe(无 HOME、只有 %USERPROFILE%)会让整张表退化成只剩数据根配置 ⇒ 内核层不再拒 `~/.ssh`,
// 而子进程里的 Git Bash 自己把 `~` 解析到 USERPROFILE、照样读得到 —— 正是“审批层拦、内核层放”的空档。
// 拒绝面宁多拦不漏;家目录未知且数据根未设(嵌入/单测)时返回空列表 —— 调用方应视为“不施加”而非“全放行”。
func CredentialDenyDirs() []string {
	var out []string
	for _, home := range UserHomes() {
		for _, d := range CredentialHomeDirs() {
			out = append(out, filepath.Join(home, d))
		}
	}
	if cfg := CredentialConfigDir(); cfg != "" {
		out = append(out, cfg)
	}
	return out
}

// LooksLikeCredentialPath 值级凭据判定(2026-09-27 审计 A2):这个**值**指的是凭据吗?
//
// 用途与边界:值级兜底对“内容/指令类参数名”(`query`/`url`/`prompt`/… ,其中的绝对路径形态
// 是内容而非路径用法)默认跳过 —— 否则 `web_search{query:"/etc/hosts"}` 这种正常调用会被拒。
// 但“内容参数名”不能成为凭据通道:`{url:"~/.ssh/id_rsa"}` 是否被当路径用虽看作者实现,读的却是
// 高价值目标,故这一类**仍然裁决**(仅限凭据面:系统路径如 `/etc/hosts` 不收紧,避免误拒搜索词)。
//
// 判据 = CredentialHomeDirs(按**家目录候选并集**的前缀判定,宁多拦不漏)+ CredentialBaseNames/Globs(按 basename);
// 与 policy-guard 的 denyPath 同源同表(单一事实源:两边都走 UserHomes),不做 I/O(纯词法,不跟 symlink)。
func LooksLikeCredentialPath(p string) bool {
	v := cleanCredPath(p)
	if v == "" {
		return false
	}
	base := filepath.Base(v)
	for _, n := range CredentialBaseNames() {
		if base == n {
			return true
		}
	}
	for _, g := range CredentialGlobs() {
		if ok, _ := filepath.Match(g, base); ok {
			return true
		}
	}
	for _, h := range UserHomes() {
		home := cleanCredPath(h)
		if home == "" || (v != home && !hasPathPrefix(v, home)) {
			continue
		}
		rel, err := filepath.Rel(home, v)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		for _, d := range CredentialHomeDirs() {
			// `.ssh` 本身或 `.ssh/**` 都算(前缀按路径段比,避免 `.sshrc` 误命中)
			if rel == d || strings.HasPrefix(rel, d+"/") {
				return true
			}
		}
	}
	if cfg := cleanCredPath(CredentialConfigDir()); cfg != "" && (v == cfg || hasPathPrefix(v, cfg)) {
		return true
	}
	return false
}

// cleanCredPath 归一化:去空白 + 展开 `~` + MSYS 盘符形态 + Clean(与 rg 类工具的写法对齐)。
//
// 家目录走 UserHome()(与用户 shell 的 `~` 同源);MSYS 盘符形态先译过再判,否则
// Windows 上 `/c/Users/u/.ssh/config` 连目录级判定都进不去(只剩 basename 名单兜)。
func cleanCredPath(p string) string {
	v := strings.TrimSpace(p)
	if v == "" {
		return ""
	}
	if v == "~" {
		return UserHome()
	}
	if strings.HasPrefix(v, "~/") || strings.HasPrefix(v, `~\`) {
		if h := UserHome(); h != "" {
			return filepath.Clean(filepath.Join(h, v[2:]))
		}
		return "" // 无家目录时无法判定 → 不误报
	}
	v = msysDrivePath(v)
	if !filepath.IsAbs(v) && !posixRooted(v) { // `/c/…`/`/etc/…` 这类 MSYS 根相对值在 Windows 上 IsAbs=false
		// 相对路径不在这里判:它经工具的 cwd 解析后是否落在凭据目录,值级无从得知
		return ""
	}
	return filepath.Clean(v)
}

// msysDrivePath 把 MSYS 盘符形态的根相对路径(`/c/Users/u/x`)译成 Windows 路径(`C:\Users\u\x`)。
//
// 只译 **`/` + 单个字母 + `/`** 这一种确定形态(Git Bash 的 `/c/` 就是 `C:\`,与 `pwd -W` 同形);
// `/etc`、`/usr` 这类映射到 Git 安装目录的写法没有静态译法,交给 basename/glob 名单兜。
// 非 Windows 语义原样返回(`winSemantics` 开关化,任意平台都能覆盖这条分支)。
func msysDrivePath(v string) string {
	if !winSemantics || len(v) < 3 || v[0] != '/' || v[2] != '/' {
		return v
	}
	c := v[1]
	if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
		return v
	}
	return strings.ToUpper(string(c)) + ":\\" + strings.ReplaceAll(v[3:], "/", `\`)
}

// hasPathPrefix 路径前缀(按段,比 strings.HasPrefix 安全:`/a/.ssh2` 不算 `/a/.ssh` 的子路径)。
func hasPathPrefix(p, prefix string) bool {
	return strings.HasPrefix(p, prefix+string(filepath.Separator))
}
