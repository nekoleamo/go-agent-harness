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

// CredentialHomeDirs 密钥目录 deny-list(相对 $HOME 判定;元素可含多级路径)。
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

// CredentialDenyDirs 内核层读拒绝的目录**绝对路径**列表($HOME 下的密钥目录 + $GAH_HOME/config)。
//
// 与 CredentialHomeDirs/CredentialConfigDir 同一份表(单一事实源):两处调用方(shell 的内核
// profile、外部进程的内核白名单)必须看到完全相同的目录集,否则“哪一侧在拦”会随维护漂移。
// 空列表 = 家目录未知且数据根未设(嵌入/单测)——调用方应视为“不施加”而非“全放行”。
func CredentialDenyDirs() []string {
	var out []string
	if home := homeDir(); home != "" {
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
// 判据 = CredentialHomeDirs(按 $HOME 前缀、逐级向上匹配)+ CredentialBaseNames/Globs(按 basename);
// 与 policy-guard 的 denyPath 同源同表(单一事实源),不做 I/O(纯词法,不跟 symlink)。
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
	home := cleanCredPath(homeDir())
	if home != "" && (v == home || hasPathPrefix(v, home)) {
		rel, err := filepath.Rel(home, v)
		if err == nil {
			rel = filepath.ToSlash(rel)
			for _, d := range CredentialHomeDirs() {
				// `.ssh` 本身或 `.ssh/**` 都算(前缀按路径段比,避免 `.sshrc` 误命中)
				if rel == d || strings.HasPrefix(rel, d+"/") {
					return true
				}
			}
		}
	}
	if cfg := cleanCredPath(CredentialConfigDir()); cfg != "" && (v == cfg || hasPathPrefix(v, cfg)) {
		return true
	}
	return false
}

// homeDir 取判定用家目录(空 = 不做目录级判定)。
func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return ""
}

// cleanCredPath 归一化:去空白 + 展开 `~` + Clean(与 rg 类工具的写法对齐)。
func cleanCredPath(p string) string {
	v := strings.TrimSpace(p)
	if v == "" {
		return ""
	}
	if v == "~" {
		return homeDir()
	}
	if strings.HasPrefix(v, "~/") || strings.HasPrefix(v, `~\`) {
		if h := homeDir(); h != "" {
			return filepath.Clean(filepath.Join(h, v[2:]))
		}
		return "" // 无家目录时无法判定 → 不误报
	}
	if !filepath.IsAbs(v) {
		// 相对路径不在这里判:它经工具的 cwd 解析后是否落在凭据目录,值级无从得知
		return ""
	}
	return filepath.Clean(v)
}

// hasPathPrefix 路径前缀(按段,比 strings.HasPrefix 安全:`/a/.ssh2` 不算 `/a/.ssh` 的子路径)。
func hasPathPrefix(p, prefix string) bool {
	return strings.HasPrefix(p, prefix+string(filepath.Separator))
}
