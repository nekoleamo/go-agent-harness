package install

import (
	"path/filepath"
	"strings"
	"testing"
)

// bareRemote 把 fixture 仓库推成一个**裸仓库**当 remote(测试里「同一个仓库换 tag」的真实形状)。
//
// 为什么不直接用工作区当 remote:`clone` 一个有 .git 的工作区也能跑,但 `ls-remote --tags`
// 在那种布局下行为与真托管商不一致(工作区的 refs 是本地 refs,不是 remote-tracking refs)。
// 裸仓库才是 git 服务端,推送/tag 的语义与 GitHub 一致。
func bareRemote(t *testing.T, repo string) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, filepath.Dir(remote), "init", "-q", "--bare", remote)
	runGit(t, repo, "remote", "add", "origin", remote)
	runGit(t, repo, "push", "-q", "origin", "HEAD:refs/heads/main")
	return remote
}

// commitAll 在 fixture 上改一行再提交(产生新 sha),返回新 sha。
func commitAll(t *testing.T, repo, msg string) string {
	t.Helper()
	writeFile(t, filepath.Join(repo, "NOTE.md"), msg)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-qm", msg)
	return gitOut(t, repo, "rev-parse", "HEAD")
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runEnv(dir, buildEnv(), "git", args...)
	if err != nil {
		t.Fatalf("git %v: %v(%s)", args, err, out)
	}
	return out
}

// TestSourceLedgerRecorded 装完必须写下「我装的是哪一份」。
func TestSourceLedgerRecorded(t *testing.T) {
	repo := makeFixture(t)
	remote := bareRemote(t, repo)
	runGit(t, repo, "tag", "v1.0.0")
	runGit(t, repo, "push", "-q", "origin", "refs/tags/v1.0.0")
	firstSHA := gitOut(t, repo, "rev-parse", "HEAD")

	home := makeHome(t)
	if _, err := Install(remote+"@v1.0.0", home); err != nil {
		t.Fatalf("Install 失败: %v", err)
	}
	ledger, err := LoadSources(home)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := ledger.Find("demo")
	if !ok {
		t.Fatalf("来源账应有 demo:%+v", ledger.All())
	}
	if e.Repo != NormalizeRepo(remote) {
		t.Errorf("repo 未归一化: %q vs %q", e.Repo, NormalizeRepo(remote))
	}
	if e.Kind != KindTag || e.Ref != "v1.0.0" {
		t.Errorf("kind/ref 不符: %+v", e)
	}
	if e.Commit != strings.ToLower(firstSHA) {
		t.Errorf("commit 应是 %s,得 %s", firstSHA, e.Commit)
	}
	if e.Origin != OriginUser {
		t.Errorf("手装的应是 user,得 %q", e.Origin)
	}
	if e.InstalledAt == "" {
		t.Errorf("应记安装时间")
	}
}

// TestTagDriftRejected 批一 §1.3 第一行:**同一 tag 解析到不同 sha ⇒ 拒绝**。
// 这是整批的核心 —— 同名 tag 可被 force-push 覆盖,用户重跑一模一样的命令就装到了别人的代码。
func TestTagDriftRejected(t *testing.T) {
	repo := makeFixture(t)
	remote := bareRemote(t, repo)
	runGit(t, repo, "tag", "v1.0.0")
	runGit(t, repo, "push", "-q", "origin", "refs/tags/v1.0.0")

	home := makeHome(t)
	if _, err := Install(remote+"@v1.0.0", home); err != nil {
		t.Fatalf("首装失败: %v", err)
	}

	// 同名 tag 被 force-push 覆盖(作者重推,或账号易手后的新人)。
	commitAll(t, repo, "second")
	runGit(t, repo, "tag", "-f", "v1.0.0")
	runGit(t, repo, "push", "-qf", "origin", "refs/tags/v1.0.0")

	before, _ := LoadSources(home)
	prevCommit, _ := before.Find("demo")

	// ① 拒绝
	_, err := Install(remote+"@v1.0.0", home)
	if err == nil {
		t.Fatal("同名 tag 被改动后必须拒绝")
	}
	for _, want := range []string{"tag", "--accept-drift"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("拒绝文案应含 %q: %v", want, err)
		}
	}
	// ② 拒绝时**不许**改盘上的东西:不能一边拒绝一边已经把产物/账覆盖了。
	after, _ := LoadSources(home)
	now, _ := after.Find("demo")
	if now.Commit != prevCommit.Commit || now.Drifted {
		t.Errorf("被拒绝的安装不应动账: before=%+v after=%+v", prevCommit, now)
	}
	// ③ --accept-drift 放行,并记 drifted
	res, err := InstallWithOpts(remote+"@v1.0.0", home, InstallOpts{AcceptDrift: true})
	if err != nil {
		t.Fatalf("accept-drift 应放行: %v", err)
	}
	if !res.Drifted {
		t.Errorf("应记 drifted")
	}
	if res.DriftWarning() == "" {
		t.Errorf("应给漂移提示文案")
	}
	ledger, _ := LoadSources(home)
	if e, _ := ledger.Find("demo"); !e.Drifted || e.Commit == prevCommit.Commit {
		t.Errorf("accept-drift 后账应记新 sha + drifted: %+v", e)
	}
}

// TestBranchDriftAllowed 分支/默认分支漂移 ⇒ **放行 + 醒目提示 + 记 drifted**(§1.3 第三行)。
// 拦 branch 等于逼所有人打 tag,而长尾作者很少打。
func TestBranchDriftAllowed(t *testing.T) {
	repo := makeFixture(t)
	remote := bareRemote(t, repo)
	home := makeHome(t)
	if _, err := Install(remote+"@main", home); err != nil {
		t.Fatalf("首装失败: %v", err)
	}
	ledger, _ := LoadSources(home)
	if e, _ := ledger.Find("demo"); e.Kind != KindBranch {
		t.Fatalf("ls-remote 应把 main 判为 branch,得 %q", e.Kind)
	}
	commitAll(t, repo, "moved")
	runGit(t, repo, "push", "-q", "origin", "HEAD:refs/heads/main")

	res, err := Install(remote+"@main", home)
	if err != nil {
		t.Fatalf("branch 漂移必须放行: %v", err)
	}
	if !res.Drifted {
		t.Errorf("应记 drifted")
	}
	ledger, _ = LoadSources(home)
	if e, _ := ledger.Find("demo"); !e.Drifted {
		t.Errorf("账上应记 drifted: %+v", e)
	}
}

// TestIdempotentReinstall 同一 ref 解析到同一 sha ⇒ 幂等重装,不判漂移。
func TestIdempotentReinstall(t *testing.T) {
	repo := makeFixture(t)
	remote := bareRemote(t, repo)
	runGit(t, repo, "tag", "v2.0.0")
	runGit(t, repo, "push", "-q", "origin", "refs/tags/v2.0.0")
	home := makeHome(t)
	if _, err := Install(remote+"@v2.0.0", home); err != nil {
		t.Fatal(err)
	}
	res, err := Install(remote+"@v2.0.0", home)
	if err != nil {
		t.Fatalf("幂等重装不应失败: %v", err)
	}
	if res.Drifted {
		t.Errorf("同一 commit 不该记 drifted")
	}
}

// TestInstallByCommitSHA §1.4:用户看到漂移拒绝后要能 `@<旧 sha>` 装回原来那版 ——
// 今天做不到(`--branch` 不接受裸 sha),那样的话拒绝文案里的第二条出路是假的。
func TestInstallByCommitSHA(t *testing.T) {
	repo := makeFixture(t)
	remote := bareRemote(t, repo)
	runGit(t, repo, "tag", "v1.0.0")
	runGit(t, repo, "push", "-q", "origin", "refs/tags/v1.0.0")
	first := gitOut(t, repo, "rev-parse", "HEAD")
	commitAll(t, repo, "second")
	runGit(t, repo, "push", "-q", "origin", "HEAD:refs/heads/main")

	home := makeHome(t)
	if _, err := Install(remote+"@"+strings.TrimSpace(first), home); err != nil {
		t.Fatalf("按 sha 安装失败: %v", err)
	}
	ledger, _ := LoadSources(home)
	e, ok := ledger.Find("demo")
	if !ok {
		t.Fatal("账上应有")
	}
	if e.Kind != KindCommit {
		t.Errorf("kind 应为 commit,得 %q", e.Kind)
	}
	if e.Commit != strings.ToLower(strings.TrimSpace(first)) {
		t.Errorf("commit 应是 %s,得 %q", first, e.Commit)
	}
	// 按同一 sha 再装一次 ⇒ 幂等(且不该触发 tag 守卫,因为 kind 是 commit)
	if _, err := Install(remote+"@"+strings.TrimSpace(first), home); err != nil {
		t.Fatalf("同 sha 重装失败: %v", err)
	}
}

// TestInstallBadSHA 写错的 sha ⇒ 明确报错,不静默换对象。
func TestInstallBadSHA(t *testing.T) {
	repo := makeFixture(t)
	remote := bareRemote(t, repo)
	home := makeHome(t)
	bad := strings.Repeat("d", 40)
	if _, err := Install(remote+"@"+bad, home); err == nil {
		t.Fatal("不存在的 sha 应报错")
	} else if !strings.Contains(err.Error(), "install:") {
		t.Errorf("报错应带 install: 前缀: %v", err)
	}
}

// TestLocalDirRecorded 本地目录安装也进来源账(kind=local),否则「我装的是什么」的答案会缺一半。
func TestLocalDirRecorded(t *testing.T) {
	repo := makeFixture(t)
	home := makeHome(t)
	res, err := Install(repo, home)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Local {
		t.Errorf("应为本地安装")
	}
	ledger, _ := LoadSources(home)
	e, ok := ledger.Find("demo")
	if !ok {
		t.Fatal("账上应有 demo")
	}
	if e.Kind != KindLocal || e.LocalPath == "" || !filepath.IsAbs(e.LocalPath) {
		t.Errorf("本地来源应记绝对路径 + kind=local: %+v", e)
	}
}

// TestUninstallClearsLedger 卸载要撤来源账(白名单撤了、账还留着 = 给不存在的插件编历史)。
func TestUninstallClearsLedger(t *testing.T) {
	repo := makeFixture(t)
	remote := bareRemote(t, repo)
	home := makeHome(t)
	if _, err := Install(remote+"@main", home); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall("demo", home, nil); err != nil {
		t.Fatal(err)
	}
	ledger, _ := LoadSources(home)
	if _, ok := ledger.Find("demo"); ok {
		t.Errorf("卸载后来源账应清空该条: %+v", ledger.All())
	}
}
