package sdk

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLooksLikeCredentialPath 值级凭据判定(2026-09-27 审计 A2)。
//
// 用途:值级兜底对内容类参数名(`query`/`url`/`prompt`/…)默认跳过,但凭据面不豁免 ——
// 这一条判定就是那个例外的唯一入口,故边界要钉死:仅凭据命中,系统路径与普通文件不碰。
func TestLooksLikeCredentialPath(t *testing.T) {
	home := t.TempDir()
	// 两家都指向同一临时目录:候选集是并集,不清干净会让本机真实 USERPROFILE 混进判定
	// (本用例要的是确定性,并集本身由 TestCredentialDenyDirsHomeUnion 钉)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GAH_HOME", filepath.Join(home, "gah-data"))

	cases := []struct {
		value string
		want  bool
		why   string
	}{
		{"~/.ssh/id_rsa", true, "凭据目录 + 密钥名"},
		{filepath.Join(home, ".ssh", "config"), true, "凭据目录(绝对路径)"},
		{"~/.ssh", true, "目录本身"},
		{"~/.aws/credentials", true, "云凭据"},
		{"~/.config/gcloud/application_default_credentials.json", true, "多级凭据目录"},
		{"~/.gnupg/secring.gpg", true, "GPG"},
		{filepath.Join(home, "gah-data", "config", "provider.yaml"), true, "gah 运行配置(含 API key)"},
		{filepath.Join(home, "proj", ".env"), true, "basename 命中"},
		{"/etc/ssl/cert.pem", true, "glob 命中(*.pem),与目录无关"},
		{filepath.Join(home, "proj", "notes.txt"), false, "普通文件"},
		{"/etc/hosts", false, "系统路径不收紧(搜索词误拒面)"},
		{"~/.sshrc", false, "前缀相近但不同目录(必须按路径段比)"},
		{"notes/todo.md", false, "相对路径不做值级判定"},
		{"", false, "空值"},
		{"https://example.com/.ssh/id_rsa", false, "URL 不在这里判(由 LooksLikePathValue 先排除)"},
	}
	for _, c := range cases {
		if got := LooksLikeCredentialPath(c.value); got != c.want {
			t.Fatalf("LooksLikeCredentialPath(%q) = %v, want %v(%s)", c.value, got, c.want, c.why)
		}
	}

	// 无 HOME 时目录级判定退化为“不误报”(basename/glob 仍然生效)
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if LooksLikeCredentialPath("~/.ssh/id_rsa") {
		t.Fatal("无 HOME 时不应把 ~ 展开成临时目录硬判")
	}
	if !LooksLikeCredentialPath("/tmp/whatever/id_rsa") {
		t.Fatal("basename 判定不依赖 HOME")
	}
}

// TestCredentialDenyDirsHomeUnion 内核层读拒绝目录必须覆盖**每一个**家目录候选。
//
// 回归的病根(2026-09-27 第二轮审计 F-A):此前 homeDir() 只读 `$HOME`,Windows 桌面壳启动的
// gah.exe 无 HOME ⇒ .ssh/.aws/… 全部从拒绝表里消失(只剩数据根 config),而子进程里的 Git Bash
// 自己把 `~` 展开到 USERPROFILE、读得到密钥 —— “审批层拦、内核层放”的空档。
func TestCredentialDenyDirsHomeUnion(t *testing.T) {
	a := filepath.Join(t.TempDir(), "home-a")
	b := filepath.Join(t.TempDir(), "home-b")
	t.Setenv("HOME", a)
	t.Setenv("USERPROFILE", b)
	t.Setenv("GAH_HOME", "") // 数据根不设:只看家目录候选这一路

	want := []string{
		filepath.Join(a, ".ssh"), filepath.Join(a, ".gnupg"), filepath.Join(a, ".aws"), filepath.Join(a, ".config", "gcloud"),
		filepath.Join(b, ".ssh"), filepath.Join(b, ".gnupg"), filepath.Join(b, ".aws"), filepath.Join(b, ".config", "gcloud"),
	}
	got := CredentialDenyDirs()
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("拒绝目录缺 %s:%v", w, got)
		}
	}

	// 只有 USERPROFILE 时仍要有密钥目录 —— 这条正是上面那个回归的直接钉法
	t.Setenv("HOME", "")
	got = CredentialDenyDirs()
	if !containsStr(got, filepath.Join(b, ".ssh")) {
		t.Fatalf("HOME 缺失(桌面壳常态)时内核层读拒绝丢了 .ssh:%v", got)
	}
}

// TestMSYSDDrivePath MSYS 盘符形态的静态译法(`/c/Users/u/x` → `C:\Users\u\x`)。
//
// 为何要有:Git Bash 里用户写的就是 `/c/Users/u/.ssh/config`;不译的话 Windows 上这倄值
// 过不了目录级比对,只剩 basename 名单兜(配置类文件如 `.ssh/config` 会整个漏掉)。
// `winSemantics` 开关化 —— 非 Windows 机器也能覆盖两侧语义。
func TestMSYSDDrivePath(t *testing.T) {
	restore := winSemantics
	t.Cleanup(func() { winSemantics = restore })

	cases := []struct {
		win  bool
		in   string
		want string
		why  string
	}{
		{true, "/c/Users/u/.ssh/config", `C:\Users\u\.ssh\config`, "盘符 + 目录级形态"},
		{true, "/C/Users/u", `C:\Users\u`, "盘符字母大小写不敏感 → 统一大写"},
		{true, "/c", "/c", "不足 `/<字母>/` 三段:不猜"},
		{true, "/cd/x", "/cd/x", "第二字符不是 `/` → 不是盘符形态"},
		{true, "/etc/hosts", "/etc/hosts", "MSYS 的 /etc 映射到 Git 安装目录,无静态译法"},
		{true, "~/x", "~/x", "`~` 已在上游展开,到这里不是盘符形态"},
		{false, "/c/Users/u", "/c/Users/u", "非 Windows 语义:原样返回"},
	}
	for _, c := range cases {
		winSemantics = c.win
		if got := msysDrivePath(c.in); got != c.want {
			t.Errorf("winSemantics=%v msysDrivePath(%q) = %q, want %q(%s)", c.win, c.in, got, c.want, c.why)
		}
	}
	winSemantics = true

	// 与判定联动:**真机断言** —— 非 Windows 上 `filepath.IsAbs("C:\\…")` 为假,译出的路径会被
	// cleanCredPath 当“不是本平台路径”丢掉(与 posixRooted 的既有权衡一致:值级只做词法判定)。
	// 这里不做跨平台跳过:断言本身按平台分区(非 Windows 期望 false)。
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GAH_HOME", "")
	if runtime.GOOS == "windows" {
		// 用盘中路径拼一个 `C:\…\Users\u` 形态的家目录,再走 MSYS 写法读其中密钥
		drive := filepath.VolumeName(home) // 例如 `C:`
		if drive == "" {
			t.Skip("家目录不在盘符卷上(CI 的临时目录可能是 UNC/相对),本条无意义")
		}
		msys := "/" + strings.ToLower(strings.TrimSuffix(drive, ":")) + "/" + strings.ReplaceAll(strings.TrimPrefix(home, drive), `\`, "/")
		t.Setenv("HOME", home)
		if !LooksLikeCredentialPath(msys + "/.ssh/config") {
			t.Fatalf("%s 应判为凭据路径(经盘符译法后落在 %s 下)", msys+"/.ssh/config", home)
		}
	} else if LooksLikeCredentialPath("/c" + home + "/.ssh/config") {
		t.Fatalf("非 Windows 上不应把 `/c…` 当成盘符路径去比对目录")
	}
}

// containsStr 小工具(避免为一次比较引入 slices 依赖;用例只求可读)。
func containsStr(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
