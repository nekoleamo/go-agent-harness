package sdk

import (
	"path/filepath"
	"testing"
)

// TestLooksLikeCredentialPath 值级凭据判定(2026-09-27 审计 A2)。
//
// 用途:值级兜底对内容类参数名(`query`/`url`/`prompt`/…)默认跳过,但凭据面不豁免 ——
// 这一条判定就是那个例外的唯一入口,故边界要钉死:仅凭据命中,系统路径与普通文件不碰。
func TestLooksLikeCredentialPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
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
	if LooksLikeCredentialPath("~/.ssh/id_rsa") {
		t.Fatal("无 HOME 时不应把 ~ 展开成临时目录硬判")
	}
	if !LooksLikeCredentialPath("/tmp/whatever/id_rsa") {
		t.Fatal("basename 判定不依赖 HOME")
	}
}
