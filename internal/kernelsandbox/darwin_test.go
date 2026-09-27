//go:build darwin

// darwin_test.go:darwin 专属用例(seatbelt profile 组装 / 前端缺失降级 / 路径转义)。
// 单独一个文件是必需的:`GOOS=linux go vet` 也会编译测试文件,而 darwinProfile 只在 darwin 定义。
package kernelsandbox

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDarwinProfileContents(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("seatbelt profile 仅 darwin")
	}
	clearMarker(t)
	root := t.TempDir()
	jail := t.TempDir()
	extra := t.TempDir()
	deny := t.TempDir()
	prof := darwinProfile(Spec{
		Mode: "workspace-write", Root: root, Jail: jail,
		RW: []string{extra, ""}, ReadDeny: []string{deny}, Label: "ut", ReadDenySwitch: "UT_CRED",
	})

	// ① 默认全放行(读 + 网络),写整体 deny 再按白名单放行 —— 与协作层"只管写"的范围一致
	if !strings.HasPrefix(prof, "(version 1)(allow default)(deny file-write*)(allow file-write*") {
		t.Fatalf("profile 头部语义不符:%s", prof)
	}
	for _, want := range []string{
		`(subpath "` + ResolvePath(root) + `")`,
		`(subpath "` + ResolvePath(jail) + `")`,
		`(subpath "` + ResolvePath(extra) + `")`,
		`(literal "/dev/null")`,
		`(subpath "/dev/fd")`,
		`(regex #"^/dev/ttys[0-9]+")`,
		`(deny file-read* (subpath "` + ResolvePath(deny) + `"))`,
	} {
		if !strings.Contains(prof, want) {
			t.Fatalf("profile 缺少 %s:\n%s", want, prof)
		}
	}
	// ② allow 分组必须闭合:否则后续 deny 会被当成 allow 表单里的参数 → "illegal argument"
	if strings.Count(prof, "(allow file-write*") != 1 || !strings.Contains(prof, `ttys[0-9]+"))`) {
		t.Fatalf("(allow file-write* …) 未正确闭合:%s", prof)
	}
	// ③ read-only 档不放行工作根
	ro := darwinProfile(Spec{Mode: "read-only", Root: root, Jail: jail, Label: "ut"})
	if strings.Contains(ro, ResolvePath(root)) {
		t.Fatalf("read-only 不应放行工作根:%s", ro)
	}
	// ④ 空项跳过(不产生 (subpath "") 这种把根当白名单的形态)
	if strings.Contains(prof, `(subpath "")`) {
		t.Fatalf("空路径项应被跳过:%s", prof)
	}
}

func TestDarwinMissingFrontendFallsBack(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("seatbelt 前端仅 darwin")
	}
	clearMarker(t)
	got := Wrap(Spec{
		Mode: "workspace-write", Root: t.TempDir(), Jail: t.TempDir(),
		SandboxExec: filepath.Join(t.TempDir(), "no-such-sandbox-exec"), Label: "ut",
	})
	if got != nil {
		t.Fatalf("前端缺失应不施加, got %v", got)
	}
}

func TestDarwinProfileQuotesHostilePaths(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("seatbelt profile 仅 darwin")
	}
	prof := darwinProfile(Spec{Mode: "read-only", Jail: `/tmp/a"b\c`, Label: "ut"})
	if !strings.Contains(prof, `a\"b\\c`) {
		t.Fatalf("路径里的引号/反斜杠必须转义(否则 profile 语法被打破):%s", prof)
	}
}
