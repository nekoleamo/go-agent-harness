// updatecheck.go:「检查更新」(批一 §1.5)—— **check-then-ask,不是自动更新**。
//
// 为什么不做自动更新(定稿结论,三条):
//
//  1. 在**没有签名**的前提下,自动更新通道是一条无认证的、持续性的远程代码执行通道。
//     手动装一次是「你读过的、你决定的」;自动更新把它变成后台持续发生、且**无法回答这个包
//     来自那个作者**。作者账号被攻陷时,手动模式只有主动重跑的人中招,自动模式**所有用户
//     同时中招**。
//  2. 它会消灭「gah 没有插件自动更新」这条正面性质 —— 风险本只在用户**主动重跑**时发生。
//  3. 与 gah 自己不一致:桌面壳刚把「检查到就自动下载安装」改成「先问再装」(`check_update` →
//     `available` → 确认 → `install_update`)。宿主自己有签名才敢问一句;插件侧不签名却要静默
//     自动,同一产品两套标准,审问时说不通。
//
// 但它背后的问题是真的:宿主升级 → 插件还是旧的那份 → 静默不兼容。那个问题的解法不是自动更新,
// 而是本文件的检查更新 + 零网络的本地兼容性提示(CompatibilityNoticeText)。
//
// 网络**只在用户点「检查更新」时发生一次**,且只发 `git ls-remote`(不 clone、不写盘)。
package install

import (
	"fmt"
	"strings"
)

// UpdateCheck 单个插件的检查结果。
type UpdateCheck struct {
	PluginID string `json:"plugin_id"`
	Repo     string `json:"repo"`
	Ref      string `json:"ref,omitempty"`
	Kind     string `json:"kind,omitempty"`
	// Current / Remote 远端与本地记录的 40 位 sha(短显示由调用方做)。
	Current string `json:"current,omitempty"`
	Remote  string `json:"remote,omitempty"`
	// Status no_update | moved | tag_changed | unreachable
	//
	// tag_changed 与 moved 分开是有意的:前者是**违反承诺**(tag 被 force-push),
	// 后者是正常的分支推进。给它们同一个状态名,面板就没法用不同措辞提醒。
	Status string `json:"status"`
	// Message 面向用户的一句话(三态各给可执行出路)。
	Message string `json:"message"`
}

// UpdateCheckStatus 取值。
const (
	// StatusNoUpdate 远端与记录一致。
	StatusNoUpdate = "no_update"
	// StatusMoved 移动引用(branch/default)前进了 —— 正常,不构成风险。
	StatusMoved = "moved"
	// StatusTagChanged **同名 tag 指向了不同的 commit** —— 按 §1.3 的规则,重装会被拒。
	StatusTagChanged = "tag_changed"
	// StatusUnreachable 问不到远端(离线/仓库删了/权限变了)—— 不是"没更新",必须区分。
	StatusUnreachable = "unreachable"
)

// CheckForUpdates 对来源账里的每个可查条目问一次远端当前指向(**不 clone**)。
//
// 为什么逐条问而不是 `git ls-remote --refs` 一次全拿:后者要在输出里按名字匹配,
// 而同名 tag 的 `refs/tags/v1` 与 `refs/tags/v1^{}`(peeled commit)两行会让人取错
// (annotated tag 的 ^{} 才是真正的 commit)。逐条问 `refs/tags/<ref>` 时 git 会自己解析
// peeled 引用 —— 一次网络往返换一个不会取错的语义,值得。
func CheckForUpdates(home string) ([]UpdateCheck, error) {
	ledger, err := LoadSources(home)
	if err != nil {
		return nil, err
	}
	var out []UpdateCheck
	for _, e := range ledger.All() {
		// 只查 git 仓库:本地目录、按 commit 固定的引用、**以及"下载来的产物"**都没有
		// "远端"可问(后者连 repo 字段存的都是一个 URL,拿它 ls-remote 会得到一条
		// 对用户毫无意义的错误,而不是"没更新")。
		if e.Kind == KindLocal || e.Kind == KindCommit || e.Kind == KindArtifact || e.Repo == "" {
			continue
		}
		out = append(out, checkOne(e))
	}
	return out, nil
}

func checkOne(e SourceEntry) UpdateCheck {
	c := UpdateCheck{PluginID: e.PluginID, Repo: e.Repo, Ref: e.Ref, Kind: e.Kind, Current: shortSHA(e.Commit)}
	// ls-remote 要的是**用户能用的那个地址**,不是归一化后的 `host/path`(后者 git 不认)。
	// 账上存归一化形态是为了让守卫不被四种拼法绕过;这里要的是能发出去的形式。
	remote := gitRemoteFor(e.Repo)
	pattern := "refs/heads/" + e.Ref
	if e.Kind == KindTag {
		pattern = "refs/tags/" + e.Ref
	}
	// 经同一条实现问 —— 本文件与 RemoteSHAOf 必须**逐字同口径**,否则「检查更新」
	// 说没变而「启用前重验」说有变,用户会先怀疑自己看错了。
	sha, err := RemoteSHAOf(e)
	if err != nil {
		c.Status = StatusUnreachable
		c.Message = fmt.Sprintf("问不到 %s(离线、仓库已删或权限变了):%v", remote, err)
		return c
	}
	if sha == "" {
		c.Status = StatusUnreachable
		c.Message = fmt.Sprintf("%s 上找不到 %s(引用被删了,或拼法对不上)", remote, pattern)
		return c
	}
	c.Remote = shortSHA(sha)
	switch {
	case sha == e.Commit:
		c.Status = StatusNoUpdate
		c.Message = "已是最新"
	case e.Kind == KindTag:
		c.Status = StatusTagChanged
		c.Message = fmt.Sprintf("**同名 %s 指向了不同的 commit**(%s → %s)。"+
			"tag 被 force-push 覆盖有两种可能:作者重推了这一版,或仓库/账号易手。"+
			"重装会被拒绝 —— 确认要换成新版就加 --accept-drift,或改成 @%s 留在原版",
			e.Ref, shortSHA(e.Commit), shortSHA(sha), e.Commit)
	default:
		c.Status = StatusMoved
		c.Message = fmt.Sprintf("%s 从 %s 前进到 %s(分支会移动,正常)。要装新版:重跑安装命令",
			e.Ref, shortSHA(e.Commit), shortSHA(sha))
	}
	return c
}

// RemoteSHAOf 问一次远端:某条来源记录里的 ref 现在指向哪个 40 位 sha(**不 clone**)。
//
// 导出的原因:启停路径(host-bridge)也要在「重新启用」时重验漂移,而它不该自己拼
// `git ls-remote` 的参数形状 —— 拼错一个 `--refs` 或拿错 peeled 引用,得到的就是
// 一个**看起来正常但其实答错了问题**的 sha。同源比同款实现更可靠。
//
// 问不到(离线/仓库删了/权限变)⇒ 返回错误,调用方决定怎么处理(不得默认成"没漂移")。
func RemoteSHAOf(e SourceEntry) (string, error) {
	if e.Repo == "" || e.Ref == "" || e.Kind == KindLocal || e.Kind == KindCommit {
		return "", fmt.Errorf("install: 来源记录不可查(%s@%s,kind=%s)", e.Repo, e.Ref, e.Kind)
	}
	pattern := "refs/heads/" + e.Ref
	if e.Kind == KindTag {
		pattern = "refs/tags/" + e.Ref
	}
	out, err := runEnv("", buildEnv(), "git", "ls-remote", "--refs", gitRemoteFor(e.Repo), pattern)
	if err != nil {
		return "", fmt.Errorf("install: 问 %s 失败: %w", e.Repo, err)
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == pattern {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("install: %s 上找不到 %s(引用被删了,或拼法对不上)", e.Repo, pattern)
}

// gitRemoteFor 把归一化的 `host/path` 还原成 git 能用的地址。
//
// 为什么不能直接 `git ls-remote host/path`(scp 形态其实**可以**,但 `host:8443/path` 不行):
// 自建 git 带端口时 scp 形态表达不了端口(见 NormalizeRepo 的说明)。带端口走 https,
// 不带端口走 scp —— 这是 git 自己也会做的选择。
//
// 绝对路径(本地仓库)原样返回:那是 kind=local 之外的一种情况(测试与自建仓库都可能是它),
// 而 `strings.Cut("/a/b", "/")` 的 host 是空串,不加这个分支就会拼出 `:a/b` 这种废地址。
func gitRemoteFor(norm string) string {
	if strings.HasPrefix(norm, "/") || strings.HasPrefix(norm, ".") {
		return norm
	}
	host, path, ok := strings.Cut(norm, "/")
	if !ok || host == "" {
		return norm
	}
	if strings.Contains(host, ":") {
		return "https://" + norm
	}
	return host + ":" + path
}
