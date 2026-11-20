package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSidecarVersionText 「关于 gah」里那一行:一致时只给版本,不一致时把偏差写出来。
//
// 为什么钉它(2026-10-30 v0.8.0 真机报告):壳与内嵌的 gah 是两个二进制,各有自己的
// `-X main.version`;构建脚本只给壳注了版本号 ⇒ 托盘「关于」说 0.8.0、Web 底栏说 vdev。
// 用户看到的是「这个包版本号不对」,而两处数字都不在同一个地方,只能靠人自己比对。
// 反向验证:把 sidecarVersionText 的「不一致」分支去掉(一律 `return "gah " + sc`),第 3 条必红。
func TestSidecarVersionText(t *testing.T) {
	cases := []struct {
		name         string
		sc, shell    string
		want         string
		wantMismatch bool
	}{
		{"一致", "0.8.1", "0.8.1", "gah 0.8.1", false},
		{"v 前缀不算不一致", "v0.8.1", "0.8.1", "gah v0.8.1", false},
		{"sidecar 报 dev", "dev", "0.8.0", "", true},
		{"取不到", "", "0.8.0", "gah(版本未取到)", false},
	}
	for _, c := range cases {
		got := sidecarVersionText(c.sc, c.shell)
		if c.wantMismatch {
			if !strings.Contains(got, "不一致") || !strings.Contains(got, c.sc) || !strings.Contains(got, c.shell) {
				t.Fatalf("%s: 不一致时要把两个版本都摊出来,得到 %q", c.name, got)
			}
			continue
		}
		if got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestSidecarBuildInjectsVersion 静态绊线:桌面发行/构建脚本给 **sidecar** 也必须注版本号。
//
// 这条不是「实现细节的复述」—— 2026-10-30 的事故就是**脚本层面**漏了一个 -X:
// 壳注了、sidecar 没注,而两个二进制在界面上都自称「版本」。
// 运行期的对账(壳日志 + 「关于」)只能**发现**;这一个是让它在构建期就红。
// 反向验证:把任一脚本的 SIDE_LD 里的 `-X main.version=$VERSION` 删掉,本用例必红
// (实测:删掉后 publish-desktop-wails.sh 真跑一次会以「sidecar 版本不对(实际 dev)」退出)。
func TestSidecarBuildInjectsVersion(t *testing.T) {
	re := regexp.MustCompile(`(?m)^SIDE_LD="[^"]*-X main\.version=\$VERSION`)
	for _, f := range []string{"publish-desktop-wails.sh", "gen-desktop-wails.sh"} {
		path := filepath.Join("..", "..", "scripts", f)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v", path, err)
		}
		if !re.Match(b) {
			t.Fatalf("%s 没给 sidecar 注入版本号(缺 SIDE_LD=\"… -X main.version=$VERSION\"):"+
				"壳会报对、内嵌的 gah 报 dev", f)
		}
	}
}
