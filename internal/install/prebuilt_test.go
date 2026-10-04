// prebuilt_test.go:批三 —— `--prebuilt` 把陌生构建脚本移出你的机器。
//
// 最要紧的一条是 TestPrebuiltNeverBuilds:**一个 shell 都不该跑**。
// 用 build_test.go 那套 `go` 桩断言它压根没被调用 —— 「没跑构建」是这个开关的全部意义,
// 而它一旦悄悄回退,用户得到的是"我以为没跑他的脚本"这个最坏的错觉。
package install

import (
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
)

// fakeBin 造一个**只有头部**的假产物(magic 指向 os/arch)。
//
// 不造一个真能跑的二进制:这个测试要验的是"下载/校验/落位/记账"这条链,
// 真跑不起来的产物反而更合适 —— 它保证如果实现哪天不小心去执行它,测试会失败。
func fakeBin(goos, goarch string) []byte {
	buf := make([]byte, 0x200)
	switch goos {
	case "linux":
		copy(buf, []byte{0x7f, 'E', 'L', 'F'})
		m := uint16(0x3e)
		if goarch == "arm64" {
			m = 0xb7
		}
		binary.LittleEndian.PutUint16(buf[18:20], m)
	case "darwin":
		buf[0], buf[1], buf[2], buf[3] = 0xcf, 0xfa, 0xed, 0xfe
		c := uint32(0x01000007)
		if goarch == "arm64" {
			c = 0x0100000c
		}
		binary.LittleEndian.PutUint32(buf[4:8], c)
	case "windows":
		buf[0], buf[1] = 'M', 'Z'
		off := uint32(0x40)
		binary.LittleEndian.PutUint32(buf[0x3c:0x40], off)
		buf[off], buf[off+1], buf[off+2], buf[off+3] = 'P', 'E', 0, 0
		m := uint16(0x8664)
		switch goarch {
		case "arm64":
			m = 0xaa64
		case "arm":
			m = 0x01c4
		}
		binary.LittleEndian.PutUint16(buf[off+4:off+6], m)
	}
	copy(buf[0x100:], []byte("not-a-real-binary"))
	return buf
}

// prebuiltServer 起一个只服务一个路径的 HTTP server。
func prebuiltServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
}

// prebuiltManifest 写一份带 prebuilt 段的 plugin.yaml(夹具仓库 + 一个额外的远程分支)。
func withPrebuilt(t *testing.T, repo string, plat, url string) {
	t.Helper()
	manifest := fmt.Sprintf("id: demo\nprotocol: bridge\nbinary: tool-demo\nprebuilt:\n  %s: %s\n", plat, url)
	writeFile(t, filepath.Join(repo, "plugin.yaml"), manifest)
}

// TestPrebuiltNeverBuilds 装预编译产物时**一个构建命令都不该跑**。
func TestPrebuiltNeverBuilds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("桩脚本是 sh,Windows 上不跑(该路径的真实安装由真机清单 W490 验)")
	}
	repo := makeFixture(t)
	srv := prebuiltServer(t, fakeBin(runtime.GOOS, runtime.GOARCH))
	defer srv.Close()
	withPrebuilt(t, repo, runtime.GOOS+"/"+runtime.GOARCH, srv.URL+"/tool-demo")

	stub := stubGo(t, t.TempDir(), true) // 桩:一旦被调用就会留下记录
	home := makeHome(t)
	res, err := InstallWithOpts(repo, home, InstallOpts{Prebuilt: true})
	if err != nil {
		t.Fatalf("预编译安装应成功: %v", err)
	}
	if stub.called("build") || stub.called("mod") || stub.called("tidy") {
		t.Fatal("--prebuilt 路径**调用了构建**(这条开关的全部意义就是不跑它)")
	}
	if res.Prebuilt == "" || !strings.HasPrefix(res.Prebuilt, srv.URL) {
		t.Errorf("结果应记下产物 URL: %+v", res)
	}
	if !strings.Contains(res.BuildCmd, "未执行构建") {
		t.Errorf("回执必须明说没跑构建: %q", res.BuildCmd)
	}
	// 产物落位
	bin := filepath.Join(home, "plugins", "demo", "tool-demo")
	if fi, err := os.Stat(bin); err != nil || fi.Size() == 0 {
		t.Fatalf("产物未落位: %v", err)
	}
}

// TestPrebuiltRecordsSameShapeAsSourcePath 与源码路径产出**同构**的记录。
//
// 为什么这条重要:来源账与白名单是「装完之后仅剩的线索」。预编译装的产物哈希同样进白名单,
// 来源账同样记 repo/ref/commit —— 否则事后看账会把"下载来的"当成"自己构建的"。
func TestPrebuiltRecordsSameShapeAsSourcePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("见 TestPrebuiltNeverBuilds")
	}
	repo := makeFixture(t)
	srv := prebuiltServer(t, fakeBin(runtime.GOOS, runtime.GOARCH))
	defer srv.Close()
	withPrebuilt(t, repo, runtime.GOOS+"/"+runtime.GOARCH, srv.URL+"/tool-demo")

	home := makeHome(t)
	if _, err := InstallWithOpts(repo, home, InstallOpts{Prebuilt: true}); err != nil {
		t.Fatal(err)
	}
	ledger, err := LoadSources(home)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := ledger.Find("demo")
	if !ok {
		t.Fatalf("来源账应有 demo: %+v", ledger.All())
	}
	if e.Prebuilt == "" || !strings.HasPrefix(e.Prebuilt, srv.URL) {
		t.Errorf("来源账应记下产物 URL(事后要能分清「下载来的」): %+v", e)
	}
	if e.Kind == "" || e.InstalledAt == "" || e.Origin != OriginUser {
		t.Errorf("其余字段应与源码路径同构: %+v", e)
	}
	// 白名单里也要有(装完再换会被下一次加载挡住 —— 这是「不签名」唯一的缓解)
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := list.Sum("tool-demo"); !ok {
		t.Error("预编译产物同样要登记白名单")
	}
	if a, ok := list.LastAuditOf("tool-demo"); !ok || !strings.HasPrefix(a.Source, "install-prebuilt:") {
		t.Errorf("审计来源应标出预编译路: %+v", a)
	}
}

// TestPrebuiltArchMismatch 架构不符 ⇒ 明确报错(**不是**「装上了跑不起来」)。
func TestPrebuiltArchMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("见 TestPrebuiltNeverBuilds")
	}
	repo := makeFixture(t)
	// 故意给一个**别的**架构
	other := "amd64"
	if runtime.GOARCH == "amd64" {
		other = "arm64"
	}
	srv := prebuiltServer(t, fakeBin(runtime.GOOS, other))
	defer srv.Close()
	withPrebuilt(t, repo, runtime.GOOS+"/"+runtime.GOARCH, srv.URL+"/tool-demo")

	home := makeHome(t)
	_, err := InstallWithOpts(repo, home, InstallOpts{Prebuilt: true})
	if err == nil {
		t.Fatal("架构不符必须报错(装上了才 exec format error 是最难查的一类)")
	}
	if !strings.Contains(err.Error(), "架构不符") || !strings.Contains(err.Error(), other) {
		t.Errorf("报错要说清「需要什么/拿到什么」: %v", err)
	}
	if fileExists(filepath.Join(home, "plugins", "demo", "tool-demo")) {
		t.Error("校验没过就不该落位")
	}
}

// TestPrebuiltEmptyArtifact 空产物 ⇒ 报错。
func TestPrebuiltEmptyArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("见 TestPrebuiltNeverBuilds")
	}
	repo := makeFixture(t)
	srv := prebuiltServer(t, nil)
	defer srv.Close()
	withPrebuilt(t, repo, runtime.GOOS+"/"+runtime.GOARCH, srv.URL+"/tool-demo")
	if _, err := InstallWithOpts(repo, makeHome(t), InstallOpts{Prebuilt: true}); err == nil {
		t.Fatal("空产物必须报错")
	} else if !strings.Contains(err.Error(), "空") {
		t.Errorf("报错要说清是空文件: %v", err)
	}
}

// TestPrebuiltHTTPError HTTP 4xx/5xx ⇒ 报错(200 但内容是错误页的那种由架构校验兜住)。
func TestPrebuiltHTTPError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("见 TestPrebuiltNeverBuilds")
	}
	repo := makeFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such asset", http.StatusNotFound)
	}))
	defer srv.Close()
	withPrebuilt(t, repo, runtime.GOOS+"/"+runtime.GOARCH, srv.URL+"/tool-demo")
	_, err := InstallWithOpts(repo, makeHome(t), InstallOpts{Prebuilt: true})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("HTTP 错误应报出来: %v", err)
	}
}

// TestPrebuiltNoEntryForPlatform 没有当前平台的产物 ⇒ **显式报错,不回退源码构建**。
//
// 这一条是本开关的诚信条款:用户点它就是为了不跑陌生脚本,悄悄回退等于把它变成谎言。
func TestPrebuiltNoEntryForPlatform(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("见 TestPrebuiltNeverBuilds")
	}
	repo := makeFixture(t)
	srv := prebuiltServer(t, fakeBin("plan9", "mips"))
	defer srv.Close()
	withPrebuilt(t, repo, "plan9/mips", srv.URL+"/tool-demo")

	_, err := InstallWithOpts(repo, makeHome(t), InstallOpts{Prebuilt: true})
	if err == nil {
		t.Fatal("没有当前平台的产物时必须报错")
	}
	for _, want := range []string{"plan9/mips", "不会退回源码构建"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("报错应含 %q: %v", want, err)
		}
	}
}

// TestPrebuiltNoSection 压根没写 prebuilt 段 ⇒ 报错并说清去掉开关就能源码构建。
func TestPrebuiltNoSection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("见 TestPrebuiltNeverBuilds")
	}
	repo := makeFixture(t) // 夹具的 plugin.yaml 没有 prebuilt 段
	_, err := InstallWithOpts(repo, makeHome(t), InstallOpts{Prebuilt: true})
	if err == nil {
		t.Fatal("没写 prebuilt 段时必须报错")
	}
	if !strings.Contains(err.Error(), "去掉 --prebuilt") {
		t.Errorf("要给出一条能走的出路: %v", err)
	}
}

// TestPrebuiltRejectsNonHTTPScheme scheme 由作者声明 ⇒ file:// 之类一律拒。
//
// 不拦的后果很具体:`file:///etc/shadow` 会把「下载一个产物」变成「读你本机的任意文件
// 并装成可执行插件」。
func TestPrebuiltRejectsNonHTTPScheme(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("见 TestPrebuiltNeverBuilds")
	}
	repo := makeFixture(t)
	withPrebuilt(t, repo, runtime.GOOS+"/"+runtime.GOARCH, "file:///etc/hosts")
	_, err := InstallWithOpts(repo, makeHome(t), InstallOpts{Prebuilt: true})
	if err == nil {
		t.Fatal("file:// 必须被拒")
	}
	if !strings.Contains(err.Error(), "http/https") {
		t.Errorf("报错要说清只收 http/https: %v", err)
	}
}

// TestArchOf 三种容器格式的魔数判定(与 scripts/gen-extplugins.sh 的 assert_arch 同源)。
func TestArchOf(t *testing.T) {
	cases := []struct {
		raw  []byte
		want string
	}{
		{fakeBin("linux", "amd64"), "linux/amd64"},
		{fakeBin("linux", "arm64"), "linux/arm64"},
		{fakeBin("darwin", "amd64"), "darwin/amd64"},
		{fakeBin("darwin", "arm64"), "darwin/arm64"},
		{fakeBin("windows", "amd64"), "windows/amd64"},
		{fakeBin("windows", "arm64"), "windows/arm64"},
		{fakeBin("windows", "arm"), "windows/arm"},
		{[]byte("plain text file, not an executable at all"), "unknown/unknown"},
	}
	for _, c := range cases {
		p := filepath.Join(t.TempDir(), "bin")
		if err := os.WriteFile(p, c.raw, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := archOf(p)
		if err != nil {
			t.Fatalf("archOf 失败: %v", err)
		}
		if got != c.want {
			t.Errorf("archOf = %q, want %q", got, c.want)
		}
	}
}

// TestWithoutPrebuiltFlagUnchanged 不带开关时行为**逐字不变**(仍是源码构建)。
func TestWithoutPrebuiltFlagUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("见 TestPrebuiltNeverBuilds")
	}
	repo := makeFixture(t)
	srv := prebuiltServer(t, fakeBin(runtime.GOOS, runtime.GOARCH))
	defer srv.Close()
	// manifest 里有 prebuilt 段,但**不带开关**装:必须仍然走源码构建(产物是真的)
	withPrebuilt(t, repo, runtime.GOOS+"/"+runtime.GOARCH, srv.URL+"/tool-demo")
	home := makeHome(t)
	res, err := Install(repo, home)
	if err != nil {
		t.Fatalf("不带开关应照常源码构建: %v", err)
	}
	if res.Prebuilt != "" {
		t.Errorf("不带开关时不该记预编译 URL: %+v", res)
	}
	if !strings.Contains(res.BuildCmd, "go build") {
		t.Errorf("不带开关时应执行默认构建命令: %q", res.BuildCmd)
	}
	// 那个"下载来的"假二进制不该被落位
	raw, err := os.ReadFile(filepath.Join(home, "plugins", "demo", "tool-demo"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "not-a-real-binary") {
		t.Error("不带开关时装上去的应是构建产物,不是下载来的")
	}
}
