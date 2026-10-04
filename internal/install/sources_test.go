package install

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNormalizeRepo 四拼法表驱动:同一个仓库的四种写法必须归一化成同一条,
// 否则用户换个写法重跑就绕过了漂移守卫(批一 §1.2)。
func TestNormalizeRepo(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/a/b", "github.com/a/b"},
		{"http://github.com/a/b", "github.com/a/b"},
		{"git://github.com/a/b", "github.com/a/b"},
		{"ssh://git@github.com/a/b.git", "github.com/a/b"},
		{"git@github.com:a/b.git", "github.com/a/b"},
		{"github.com/a/b", "github.com/a/b"},
		{"https://github.com/a/b/", "github.com/a/b"},
		{"https://GitHub.com/a/b", "github.com/a/b"},
		{"https://github.com/a/B", "github.com/a/B"}, // 路径大小写不动
		{"HTTPS://GITHUB.COM/a/b.git", "github.com/a/b"},
		// 自建 git 带端口:端口是两个不同仓库的一部分,必须保留。
		{"https://git.example.com:8443/a/b", "git.example.com:8443/a/b"},
		{"ssh://git@git.example.com:8443/a/b.git", "git.example.com:8443/a/b"},
		{"git@git.example.com:a/b.git", "git.example.com/a/b"},
		// 带 @ 的仓库路径不能被当成 user@host。
		{"github.com/a/b@v1.2.0", "github.com/a/b@v1.2.0"},
		{"  https://github.com/a/b  ", "github.com/a/b"},
		// 本地路径**原样**:`.git` 剥离对网络标识是规范化,对本地路径是破坏性的
		// (`/srv/remote.git` → `/srv/remote`,那目录不存在)。`-install <本地路径>@ref`
		// 是支持的入口,那样记账会让「检查更新」永远 unreachable(CI 在 Linux 上抓到的)。
		{"/srv/git/remote.git", "/srv/git/remote.git"},
		{"./local/remote.git", "local/remote.git"},
		{"../up/remote.git", "../up/remote.git"},
		{"/srv/git/remote/", "/srv/git/remote"},
		{"https://gitee.com/nekoleamo/go-agent-harness.git", "gitee.com/nekoleamo/go-agent-harness"},
	}
	for _, c := range cases {
		if got := NormalizeRepo(c.in); got != c.want {
			t.Errorf("NormalizeRepo(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSplitSpec 拆 <repo>[@<ref>]:必须用**最后一个** @ —— `git@github.com:a/b` 里的 @ 不是 ref。
func TestSplitSpec(t *testing.T) {
	cases := []struct{ in, repo, ref string }{
		{"github.com/a/b", "github.com/a/b", ""},
		{"github.com/a/b@v1.2.0", "github.com/a/b", "v1.2.0"},
		{"git@github.com:a/b.git@v1.2.0", "git@github.com:a/b.git", "v1.2.0"},
		{"github.com/a/b@", "github.com/a/b@", ""},                 // 尾部 @ 不算 ref 分隔符
		{"https://github.com/a/b/", "https://github.com/a/b/", ""}, // ref 只认 @ref,不打的主干不算,
	}
	for _, c := range cases {
		repo, ref := SplitSpec(c.in)
		if repo != c.repo || ref != c.ref {
			t.Errorf("SplitSpec(%q) = (%q,%q), want (%q,%q)", c.in, repo, ref, c.repo, c.ref)
		}
	}
}

func TestIsCommitSHA(t *testing.T) {
	full := strings.Repeat("a", 40)
	if !IsCommitSHA(full) {
		t.Errorf("40 位 hex 应判为 commit")
	}
	// 短 sha 一律不认:--branch 认不了短 sha,且短 sha 在不同 clone 下可能指不同对象。
	for _, s := range []string{"", "v1.2.0", strings.Repeat("a", 39), strings.Repeat("a", 41), "main", strings.Repeat("z", 40)} {
		if IsCommitSHA(s) {
			t.Errorf("IsCommitSHA(%q) = true, want false", s)
		}
	}
}

// TestClassifyRef 无网络判定形态。
func TestClassifyRef(t *testing.T) {
	cases := []struct{ ref, want string }{
		{"", KindDefault},
		{"v1.2.0", KindTag},
		{"main", KindTag}, // 形态上无法区分 branch/tag ⇒ 保守按 tag(见 ClassifyRef 注释)
		{strings.Repeat("b", 40), KindCommit},
	}
	for _, c := range cases {
		if got := ClassifyRef(c.ref); got != c.want {
			t.Errorf("ClassifyRef(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}

// TestCheckDrift 批一 §1.3 的四行表。
func TestCheckDrift(t *testing.T) {
	prev := SourceEntry{Repo: "github.com/a/b", Ref: "v1.2.0", Kind: KindTag, Commit: "1111111111111111111111111111111111111111"}

	// 同 ref 解析到相同 sha ⇒ 幂等重装,不判漂移。
	if v := CheckDrift(prev, "v1.2.0", prev.Commit, false); v.Reject || v.Drifted {
		t.Errorf("同 commit 应放行且不记漂移, got %+v", v)
	}
	// tag 漂移 ⇒ 拒绝,文案给两条出路。
	v := CheckDrift(prev, "v1.2.0", "2222222222222222222222222222222222222222", false)
	if !v.Reject {
		t.Fatalf("tag 漂移必须拒绝, got %+v", v)
	}
	for _, want := range []string{"--accept-drift", "@1111111111111111111111111111111111111111"} {
		if v.Err == nil || !strings.Contains(v.Err.Error(), want) {
			t.Errorf("拒绝文案缺出路 %q: %v", want, v.Err)
		}
	}
	// --accept-drift ⇒ 放行并记漂移。
	if v := CheckDrift(prev, "v1.2.0", "2222222222222222222222222222222222222222", true); v.Reject || !v.Drifted {
		t.Errorf("accept-drift 应放行且记 drifted, got %+v", v)
	}
	// branch 漂移 ⇒ 放行 + 醒目提示,记 drifted。
	br := SourceEntry{Repo: "github.com/a/b", Ref: "main", Kind: KindBranch, Commit: "1111111111111111111111111111111111111111"}
	v = CheckDrift(br, "main", "3333333333333333333333333333333333333333", false)
	if v.Reject || !v.Drifted {
		t.Errorf("branch 漂移应放行且记 drifted, got %+v", v)
	}
	// 首装(账上没这一条)⇒ 无判定。
	if v := CheckDrift(SourceEntry{}, "v1.0.0", "4444444444444444444444444444444444444444", false); v.Reject || v.Drifted {
		t.Errorf("首装不应判漂移, got %+v", v)
	}
	// rev-parse 失败(resolvedSHA 空)⇒ 不阻断、不判漂移(降级为 commit 未知)。
	if v := CheckDrift(prev, "v1.2.0", "", false); v.Reject {
		t.Errorf("解析不到 sha 时不应阻断安装, got %+v", v)
	}
}

func TestSourcesRecordRemove(t *testing.T) {
	var sf SourceLedger
	sf.Record(SourceEntry{PluginID: "b", Repo: "github.com/x/b"})
	sf.Record(SourceEntry{PluginID: "a", Repo: "github.com/x/a"})
	sf.Record(SourceEntry{PluginID: "b", Repo: "github.com/y/b"})
	if len(sf.Sources) != 2 {
		t.Fatalf("同 id 应覆盖, got %d 条", len(sf.Sources))
	}
	e, ok := sf.Find("b")
	if !ok || e.Repo != "github.com/y/b" {
		t.Errorf("Find(b) = %+v", e)
	}
	if got := sf.All(); got[0].PluginID != "a" {
		t.Errorf("应按 id 排序, got %+v", got)
	}
	sf.Remove("b")
	sf.Remove("b") // 幂等
	if _, ok := sf.Find("b"); ok {
		t.Errorf("Remove 后不应还在")
	}
}

// TestSourcesRoundTrip 0600 + 幂等往返。
func TestSourcesRoundTrip(t *testing.T) {
	home := t.TempDir()
	sf, err := LoadSources(home)
	if err != nil {
		t.Fatalf("首读应为空账不报错: %v", err)
	}
	if len(sf.Sources) != 0 {
		t.Fatalf("首读应为空")
	}
	sf.Record(SourceEntry{Repo: "github.com/a/b", Ref: "v1.2.0", Kind: KindTag,
		Commit: "9f2c1ab3e5f7", PluginID: "demo", APIVersion: "v1", Origin: OriginUser, InstalledAt: nowRFC()})
	if err := WriteSources(home, sf); err != nil {
		t.Fatalf("写账失败: %v", err)
	}
	fi, err := os.Stat(SourcesPath(home))
	if err != nil {
		t.Fatal(err)
	}
	// 0600 只在 POSIX 形态上可判:Windows 的权限位由 ACL 管,os.Chmod(0o600) 在那里
	// 不产生该权限位(AGENTS.md 跨平台坑 ③)。写死这条就是让 test-windows 稳定红
	// —— v0.5.1 的 CI 红里就有这一条。
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("来源账应 0600, got %v", fi.Mode().Perm())
	}
	back, err := LoadSources(home)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := back.Find("demo")
	if !ok || e.Ref != "v1.2.0" || e.Commit != "9f2c1ab3e5f7" || e.APIVersion != "v1" {
		t.Errorf("往返丢字段: %+v", e)
	}
}

// TestSourcesBrokenIsError **回归护栏**(批一 §1.7):坏账必须显式报错,
// 既不"既报错又装"(那是调用方的事)、更不静默覆盖旧记录 —— 静默当空账的话,
// 下一次安装会把整份旧记录 WriteSources 掉,而那份记录是唯一能回答"上周那个插件是哪一份"的东西。
func TestSourcesBrokenIsError(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SourcesPath(home), []byte("sources: [oops\n  - bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSources(home); err == nil {
		t.Fatal("坏账必须报错(不得静默当空账覆盖)")
	} else if !strings.Contains(err.Error(), SourcesFile) {
		t.Errorf("报错应指明是哪个文件: %v", err)
	}
}
