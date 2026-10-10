// clone.go:拉取(批一 §1.4)。三件事在这里,因为它们共享同一个入口 cloneRepo。
//
// ① `@<ref>` 仍是 `git clone --depth 1 --branch <ref>`(快)。
//
// ② **`@<40位 sha>` 支持** —— 这是批一之前**做不到**的事:`--branch` 不接受裸 sha。
//
//	没有它,用户看到 tag 漂移的拒绝文案里「装回原来那版」那一条出路是**假的**。
//	浅 fetch + `checkout FETCH_HEAD`;托管商不允许对任意历史 commit 浅 fetch 时,
//	退回完整 clone(慢,但正确 —— 正确性优先于速度)。
//
// ③ 环境同样清洗(与 build.go 同款):git 也可能在 remote helper / credential helper 里
//
//	读到宿主凭据。HOME 与 SSH_AUTH_SOCK 由 SanitizedEnv 保留 ⇒ SSH 私库照常可用。
//
// 解析实际落地的 sha(`git rev-parse HEAD`)**失败不阻断安装**:那只是「来源账记不下具体
// commit」。把它升级成错误会让一个可用的安装因为一条诊断信息失败;但也**不能**猜一个值填进去
// —— 猜出来的 commit 比空值更危险(它会让漂移守卫拿一个错误的基线判)。
package install

import (
	"fmt"
	"github.com/nekoleamo/go-agent-harness/sdk"
	"strings"
)

// cloneRepo 把 repo 拉到 clone 目录(ref 空 = 默认分支)。
func cloneRepo(repo, ref, clone string) error {
	if IsCommitSHA(ref) {
		return cloneBySHA(repo, ref, clone)
	}
	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, repo, clone)
	if out, err := runEnv("", buildEnv(), "git", args...); err != nil {
		return fmt.Errorf("install: 拉取 %s: %w(%s)", repo, err, out)
	}
	return nil
}

// cloneBySHA 按 40 位 sha 拉取(§1.4):先浅 clone + 浅 fetch + checkout FETCH_HEAD,
// 失败退回完整 clone + checkout。
//
// 为什么先浅后全:完整 clone 会把整个历史拉下来(长尾插件可能是几百 MB),而绝大多数
// 托管商(GitHub / Gitee / GitLab)都允许 `git fetch --depth 1 origin <sha>`。
// 只有服务端禁了 uploadpack.allowReachableSHA1InWant / allowAnySHA1InWant 时才走全量。
func cloneBySHA(repo, sha, clone string) error {
	if out, err := runEnv("", buildEnv(), "git", "clone", "--depth", "1", repo, clone); err != nil {
		return fmt.Errorf("install: 拉取 %s: %w(%s)", repo, err, out)
	}
	fetchArgs := []string{"fetch", "--depth", "1", "origin", sha}
	if _, err := runEnv(clone, buildEnv(), "git", fetchArgs...); err == nil {
		if _, err2 := runEnv(clone, buildEnv(), "git", "checkout", "-q", "FETCH_HEAD"); err2 == nil {
			return nil
		}
		// fetch 成功但 checkout 失败:通常是 FETCH_HEAD 不存在。
		// 不静默 —— 退回全量路径,那里会报一条能看懂的错。
	}
	// 退回完整 clone。
	if err := sdk.RemoveTree(clone); err != nil {
		return err
	}
	if out, err := runEnv("", buildEnv(), "git", "clone", repo, clone); err != nil {
		return fmt.Errorf("install: 拉取 %s(完整历史): %w(%s)", repo, err, out)
	}
	if out, err := runEnv(clone, buildEnv(), "git", "checkout", "-q", sha); err != nil {
		return fmt.Errorf("install: 该 commit %s 需要完整历史才能 checkout,且完整拉取后仍找不到它"+
			"(sha 可能写错,或它已被仓库 gc 掉): %w(%s)", shortSHA(sha), err, out)
	}
	return nil
}

// resolveCommit 取 clone 里实际落地的 40 位 sha。**失败返回 ""**(见包注释)。
func resolveCommit(clone string) string {
	out, err := runEnv(clone, buildEnv(), "git", "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	out = strings.TrimSpace(out)
	if !IsCommitSHA(out) {
		return ""
	}
	return strings.ToLower(out)
}

// resolveKind 区分 ref 是 branch 还是 tag(`git clone --branch` 两者都不区分)。
//
// 只问一次 `ls-remote --tags refs/tags/<ref>`:命中 = tag,没命中 = branch。
// 问不动(离线/自建关掉了 ls-remote/权限)⇒ 沿用传入的形态判定(保守按 tag),
// 并在 kind 上不假装确定 —— 见 ClassifyRef 的注释:宁可多拒一次让用户加 --accept-drift,
// 也不能把被 force-push 的 tag 当成会移动的 branch 放过去。
func resolveKind(repo, ref, clone, fallback string) string {
	if ref == "" || IsCommitSHA(ref) {
		return fallback
	}
	out, err := runEnv(clone, buildEnv(), "git", "ls-remote", "--tags", "origin", "refs/tags/"+ref)
	if err != nil {
		return fallback
	}
	if strings.TrimSpace(out) != "" {
		return KindTag
	}
	// 没命中 tag ⇒ branch(也可能是默认分支名);两者都按「会移动」处理。
	return KindBranch
}
