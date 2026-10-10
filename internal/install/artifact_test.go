// artifact_test.go:批七 —— `gah -install-artifact <url> --id <id> --name <name>`。
//
// 三件要钉住:
//  1. **路径注入**:`--id` / `--name` 会直接拼进落位路径,这是本入口最大的攻击面;
//  2. **不构建**:全程不碰 Go / node / make(与 `--prebuilt` 同一条纪律);
//  3. **与另外两条装法产出同构的记录**:白名单 + 来源账 + 审计,否则事后翻不出它从哪来。
package install

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/internal/testutil"
)

func artifactServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
}

// TestValidateArtifactArgsRejectsPathInjection id/name 会拼进落位路径 ⇒ 白名单式校验。
//
// 这是本入口最大的攻击面:`--id ../..` 或 `--name ../../evil` 拼进 filepath.Join
// 就能写到 plugins/ 外面去。白名单(只允许列出的字符 + 首字符非点)比黑名单可靠。
func TestValidateArtifactArgsRejectsPathInjection(t *testing.T) {
	bad := []struct{ id, name, why string }{
		{"../evil", "tool-x", "id 含 .."},
		{"..", "tool-x", "id 就是 .."},
		{"a/b", "tool-x", "id 含路径分隔符"},
		{"a\\b", "tool-x", "id 含反斜杠"},
		{"demo", "../../evil", "name 含 .."},
		{"demo", "a/tool-x", "name 含路径分隔符"},
		{"", "tool-x", "id 为空"},
		{"demo", "", "name 为空"},
		{".hidden", "tool-x", "id 以点开头"},
		{"demo", ".tool-x", "name 以点开头"},
		{strings.Repeat("x", 65), "tool-x", "id 超长"},
		{"demo", strings.Repeat("x", 65), "name 超长"},
		{"demo", "tool-x\n", "name 含换行"},
	}
	for _, c := range bad {
		if err := ValidateArtifactArgs(c.id, c.name); err == nil {
			t.Errorf("ValidateArtifactArgs(%q,%q) 应拒绝(%s)", c.id, c.name, c.why)
		}
	}
	// 合法的
	for _, c := range [][2]string{{"demo", "tool-demo"}, {"my.plugin_2", "cmd-kit"}, {"d", "tool-d"}} {
		if err := ValidateArtifactArgs(c[0], c[1]); err != nil {
			t.Errorf("ValidateArtifactArgs(%q,%q) 应放行: %v", c[0], c[1], err)
		}
	}
}

// TestValidateArtifactArgsRequiresLoaderPrefix 名字必须用**加载器自己的**判据。
//
// 另立一套规则就会出现「装的时候过了、加载时被静默跳过」。
func TestValidateArtifactArgsRequiresLoaderPrefix(t *testing.T) {
	for _, name := range []string{"echo", "mytool", "Tool-demo", "plugin.exe"} {
		if err := ValidateArtifactArgs("demo", name); err == nil {
			t.Errorf("%q 不以 tool-/cmd- 开头,应被拒", name)
		}
	}
	if err := ValidateArtifactArgs("demo", "tool-echo"); err != nil {
		t.Errorf("tool- 前缀应放行: %v", err)
	}
}

// TestInstallArtifactNoBuild 全程不碰构建工具链。
func TestInstallArtifactNoBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("桩脚本是 sh,Windows 上不跑(该路径的真实安装由真机清单验)")
	}
	// 把 PATH 指向一个**只有桩、没有 go** 的目录:若实现去调任何构建命令,这里会失败。
	stub := stubGo(t, t.TempDir(), true)
	t.Setenv("PATH", filepath.Dir(stub.marker)+string(os.PathListSeparator)+"/nonexistent")

	srv := artifactServer(t, fakeBin(runtime.GOOS, runtime.GOARCH))
	defer srv.Close()
	home := makeHome(t)
	res, err := InstallArtifact(srv.URL+"/tool-demo", "demo", "tool-demo", home)
	if err != nil {
		t.Fatalf("产物安装应成功: %v", err)
	}
	if stub.called("build") || stub.called("mod") {
		t.Fatal("产物安装路径调用了构建 —— 这条入口的全部意义就是不跑构建")
	}
	if !res.Artifact || res.Prebuilt == "" {
		t.Errorf("结果应标出这是产物安装: %+v", res)
	}
	if !strings.Contains(res.BuildCmd, "未执行构建") {
		t.Errorf("回执要明说没跑构建: %q", res.BuildCmd)
	}
	bin := filepath.Join(home, "plugins", "demo", testutil.ExeName("tool-demo"))
	fi, err := os.Stat(bin)
	if err != nil || fi.Size() == 0 {
		t.Fatalf("产物未落位: %v", err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("落位产物应可执行: %v", fi.Mode().Perm())
	}
}

// TestInstallArtifactRecordsSameShape 白名单 + 来源账 + 审计,三样都要有。
func TestInstallArtifactRecordsSameShape(t *testing.T) {
	srv := artifactServer(t, fakeBin(runtime.GOOS, runtime.GOARCH))
	defer srv.Close()
	home := makeHome(t)
	if _, err := InstallArtifact(srv.URL+"/tool-demo", "demo", "tool-demo", home); err != nil {
		t.Fatal(err)
	}
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	sum, ok := list.Sum(testutil.ExeName("tool-demo"))
	if !ok {
		t.Fatal("产物应登记进白名单")
	}
	// 装完再换会被挡住 —— 这是不验签名下**唯一**的缓解,必须真的生效
	if err := list.Verify(testutil.ExeName("tool-demo"), sum); err != nil {
		t.Errorf("刚装的那份应通过校验: %v", err)
	}
	if err := list.Verify(testutil.ExeName("tool-demo"), [32]byte{0xAB}); err == nil {
		t.Error("换成别的内容应被挡住(否则「不验签名」就连这一点缓解都没有)")
	}
	if a, ok := list.LastAuditOf(testutil.ExeName("tool-demo")); !ok || !strings.HasPrefix(a.Source, "artifact-install:") {
		t.Errorf("审计来源应标出产物安装: %+v", a)
	}
	// 来源账
	ledger, err := LoadSources(home)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := ledger.Find("demo")
	if !ok {
		t.Fatalf("来源账应有 demo: %+v", ledger.All())
	}
	if e.Kind != KindArtifact || e.Origin != OriginUser || e.Prebuilt == "" {
		t.Errorf("来源账应记 kind=artifact / origin=user / 产物 URL: %+v", e)
	}
	// 产品路径不做漂移检查(它连 repo 都不是)
	checks, err := CheckForUpdates(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 0 {
		t.Errorf("产物来源不该进更新检查(没有远端可问): %+v", checks)
	}
}

// TestInstallArtifactArchMismatch 架构不符 ⇒ 明确报错,不是「装上了跑不起来」。
func TestInstallArtifactArchMismatch(t *testing.T) {
	other := "amd64"
	if runtime.GOARCH == "amd64" {
		other = "arm64"
	}
	srv := artifactServer(t, fakeBin(runtime.GOOS, other))
	defer srv.Close()
	home := makeHome(t)
	_, err := InstallArtifact(srv.URL+"/tool-demo", "demo", "tool-demo", home)
	if err == nil {
		t.Fatal("架构不符必须报错")
	}
	if !strings.Contains(err.Error(), "架构不符") || !strings.Contains(err.Error(), other) {
		t.Errorf("报错要说清需要什么/拿到什么: %v", err)
	}
	if fileExists(filepath.Join(home, "plugins", "demo", testutil.ExeName("tool-demo"))) {
		t.Error("校验没过就不该落位")
	}
}

// TestInstallArtifactEmptyAndHTTPError 下载层的失败各自报各自的错。
func TestInstallArtifactEmptyAndHTTPError(t *testing.T) {
	home := makeHome(t)
	empty := artifactServer(t, nil)
	_, err := InstallArtifact(empty.URL+"/x", "demo", "tool-demo", home)
	empty.Close()
	if err == nil || !strings.Contains(err.Error(), "空") {
		t.Errorf("空产物应报错: %v", err)
	}
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer notFound.Close()
	if _, err := InstallArtifact(notFound.URL+"/x", "demo", "tool-demo", home); err == nil ||
		!strings.Contains(err.Error(), "404") {
		t.Errorf("HTTP 错误应报出来: %v", err)
	}
}

// TestInstallArtifactRejectsNonHTTPScheme file:// 之类一律拒(与 prebuilt 同口径)。
func TestInstallArtifactRejectsNonHTTPScheme(t *testing.T) {
	_, err := InstallArtifact("file:///etc/hosts", "demo", "tool-demo", makeHome(t))
	if err == nil || !strings.Contains(err.Error(), "http/https") {
		t.Fatalf("file:// 应被拒: %v", err)
	}
}

// TestArtifactConfirmTextStatesTheCost 确认文案必须讲清「下载来的、没有独立校验」。
func TestArtifactConfirmTextStatesTheCost(t *testing.T) {
	p := ConfirmPromptArtifact(ArtifactFacts{
		URL: "https://example.invalid/tool-demo", ID: "demo", Name: "tool-demo",
		Dir: "/x/plugins/demo",
	})
	for _, want := range []string{"不会执行任何构建命令", "不验签名", "没有独立校验", "装完再换"} {
		if !strings.Contains(p, want) {
			t.Errorf("确认文案缺 %q:\n%s", want, p)
		}
	}
}
