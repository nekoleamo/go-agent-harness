package install

import (
	"strings"
	"testing"
)

func TestGitRemoteFor(t *testing.T) {
	cases := []struct{ in, want string }{
		{"github.com/a/b", "github.com:a/b"},                             // 无端口 ⇒ scp 形态
		{"git.example.com:8443/a/b", "https://git.example.com:8443/a/b"}, // 带端口 ⇒ https
	}
	for _, c := range cases {
		if got := gitRemoteFor(c.in); got != c.want {
			t.Errorf("gitRemoteFor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCheckForUpdates 三态:无更新 / 分支前移 / **同名 tag 被改**。
// 最后一态是这一整批存在的理由 —— 它对应「重跑一模一样的命令装到了别人的代码」。
func TestCheckForUpdates(t *testing.T) {
	repo := makeFixture(t)
	remote := bareRemote(t, repo)
	runGit(t, repo, "tag", "v1.0.0")
	runGit(t, repo, "push", "-q", "origin", "refs/tags/v1.0.0")
	home := makeHome(t)
	if _, err := Install(remote+"@v1.0.0", home); err != nil {
		t.Fatal(err)
	}
	byID := func() UpdateCheck {
		cs, err := CheckForUpdates(home)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			if c.PluginID == "demo" {
				return c
			}
		}
		t.Fatalf("检查结果里没有 demo: %+v", cs)
		return UpdateCheck{}
	}
	if got := byID(); got.Status != StatusNoUpdate {
		t.Fatalf("刚装完应是无更新,得 %+v", got)
	}

	// 同名 tag 被 force-push ⇒ tag_changed,且文案要给两条出路。
	commitAll(t, repo, "hijack")
	runGit(t, repo, "tag", "-f", "v1.0.0")
	runGit(t, repo, "push", "-qf", "origin", "refs/tags/v1.0.0")
	got := byID()
	if got.Status != StatusTagChanged {
		t.Fatalf("tag 被改应报 tag_changed,得 %+v", got)
	}
	for _, want := range []string{"--accept-drift", "@"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("tag_changed 文案应含 %q: %s", want, got.Message)
		}
	}

	// 引用被删 ⇒ unreachable(**不是** no_update:那会让面板显示"已是最新")。
	runGit(t, repo, "push", "-q", "origin", ":refs/tags/v1.0.0")
	if got := byID(); got.Status != StatusUnreachable {
		t.Errorf("引用消失应报 unreachable,得 %+v", got)
	}
}

// TestCheckForUpdatesBranchMoved 分支前移 = 正常,不是风险(拦 branch 等于逼所有人打 tag)。
func TestCheckForUpdatesBranchMoved(t *testing.T) {
	repo := makeFixture(t)
	remote := bareRemote(t, repo)
	home := makeHome(t)
	if _, err := Install(remote+"@main", home); err != nil {
		t.Fatal(err)
	}
	commitAll(t, repo, "advance")
	runGit(t, repo, "push", "-q", "origin", "HEAD:refs/heads/main")
	cs, err := CheckForUpdates(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Status != StatusMoved {
		t.Fatalf("分支前移应报 moved: %+v", cs)
	}
}

// TestCheckForUpdatesSkipsLocal 本地目录与 commit 引用没有"远端",不该被列成查不到。
func TestCheckForUpdatesSkipsLocal(t *testing.T) {
	home := makeHome(t)
	var ledger SourceLedger
	ledger.Record(SourceEntry{PluginID: "loc", Kind: KindLocal, Repo: "/src/loc"})
	ledger.Record(SourceEntry{PluginID: "pin", Kind: KindCommit, Repo: "github.com/a/b", Ref: "0123456789abcdef0123456789abcdef01234567"})
	if err := WriteSources(home, &ledger); err != nil {
		t.Fatal(err)
	}
	cs, err := CheckForUpdates(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 0 {
		t.Fatalf("本地/commit 引用不该进检查列表: %+v", cs)
	}
}

// TestCompatibilityNotice 零网络的本地兼容性提示。
func TestCompatibilityNotice(t *testing.T) {
	home := makeHome(t)
	var ledger SourceLedger
	ledger.Record(SourceEntry{PluginID: "ok", Kind: KindTag, APIVersion: "v1"})
	ledger.Record(SourceEntry{PluginID: "old", Kind: KindTag, APIVersion: "v0.9"})
	ledger.Record(SourceEntry{PluginID: "undeclared", Kind: KindTag}) // 未声明 = v1
	if err := WriteSources(home, &ledger); err != nil {
		t.Fatal(err)
	}
	bad, err := CompatibilityNotice(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 1 || bad[0].PluginID != "old" {
		t.Fatalf("应只报 old: %+v", bad)
	}
	txt := CompatibilityNoticeText(home)
	if !strings.Contains(txt, "1 个插件") || !strings.Contains(txt, "old") {
		t.Errorf("汇总文案不对: %s", txt)
	}
	// 账上没有不兼容项 ⇒ 空串(前端据此不显示横幅)。
	var clean SourceLedger
	clean.Record(SourceEntry{PluginID: "ok", Kind: KindTag, APIVersion: "v1"})
	if err := WriteSources(home, &clean); err != nil {
		t.Fatal(err)
	}
	if got := CompatibilityNoticeText(home); got != "" {
		t.Errorf("无问题时应为空串,得 %q", got)
	}
}
