// 路径解析器逃逸回归(12+ 例):绝对/相对 ../ / symlink / deny-list / 空路径 / NUL / 目录 / 不存在。
package hostdocview

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeSandbox 最小沙箱实现(仅测路径策略)。
type fakeSandbox struct {
	mode sdk.SandboxMode
	root string
}

func (f *fakeSandbox) Mode() sdk.SandboxMode     { return f.mode }
func (f *fakeSandbox) SetMode(m sdk.SandboxMode) { f.mode = m }
func (f *fakeSandbox) Root() string              { return f.root }
func (f *fakeSandbox) ValidatePath(string) error { return nil }

// newFixture 构造:workspace/(含文件)、outside/(外部文件)、home/{config,attachments}。
func newFixture(t *testing.T) (ws, outside, home string) {
	t.Helper()
	base := t.TempDir()
	ws = filepath.Join(base, "workspace")
	outside = filepath.Join(base, "outside")
	home = filepath.Join(base, "gah-data")
	for _, d := range []string{ws, outside, filepath.Join(home, "config"), filepath.Join(home, "attachments")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{
		filepath.Join(ws, "note.md"),
		filepath.Join(ws, ".env"),
		filepath.Join(ws, "id_rsa"),
		filepath.Join(ws, "cert.pem"),
		filepath.Join(outside, "secret.txt"),
		filepath.Join(home, "config", "provider.yaml"),
		filepath.Join(home, "attachments", "pic.png"),
	} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// symlink:workspace/inside-link → workspace 内(outside-link → workspace 外)
	if err := os.Symlink(filepath.Join(ws, "note.md"), filepath.Join(ws, "inside-link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(ws, "outside-link.md")); err != nil {
		t.Fatal(err)
	}
	// 目录以链接形式出现在 workspace 内(应拒绝:目标是目录)
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return ws, outside, home
}

func TestResolveEscapeMatrix(t *testing.T) {
	ws, outside, home := newFixture(t)
	abs := func(p string) string { return filepath.Join(p) }

	cases := []struct {
		name    string
		path    string
		strict  bool
		mode    sdk.SandboxMode
		wantOK  bool
		wantErr error
	}{
		{"相对路径 workspace 内", "note.md", false, sdk.SandboxWorkspace, true, nil},
		{"相对路径带 ..", "../outside/secret.txt", false, sdk.SandboxWorkspace, false, sdk.ErrDocDenied},
		{"相对路径内嵌 ..", "sub/../../outside/secret.txt", false, sdk.SandboxWorkspace, false, sdk.ErrDocDenied},
		{"绝对路径 workspace 内", abs(filepath.Join(ws, "note.md")), false, sdk.SandboxWorkspace, true, nil},
		{"绝对路径 workspace 外(workspace-write)", abs(filepath.Join(outside, "secret.txt")), false, sdk.SandboxWorkspace, false, sdk.ErrDocDenied},
		{"绝对路径 workspace 外(full-access)", abs(filepath.Join(outside, "secret.txt")), false, sdk.SandboxFullAccess, true, nil},
		{"symlink 指向 workspace 内", "inside-link.md", false, sdk.SandboxWorkspace, true, nil},
		{"symlink 逃逸出 workspace", "outside-link.md", false, sdk.SandboxWorkspace, false, sdk.ErrDocDenied},
		{"strict: 绝对路径 workspace 外", abs(filepath.Join(outside, "secret.txt")), true, sdk.SandboxFullAccess, false, sdk.ErrDocDenied},
		{"strict: symlink 逃逸", "outside-link.md", true, sdk.SandboxFullAccess, false, sdk.ErrDocDenied},
		{"strict: 数据根 config/", abs(filepath.Join(home, "config", "provider.yaml")), true, sdk.SandboxFullAccess, false, sdk.ErrDocDenied},
		{"strict: .env", abs(filepath.Join(ws, ".env")), true, sdk.SandboxFullAccess, false, sdk.ErrDocDenied},
		{"strict: id_rsa", abs(filepath.Join(ws, "id_rsa")), true, sdk.SandboxFullAccess, false, sdk.ErrDocDenied},
		{"strict: *.pem", abs(filepath.Join(ws, "cert.pem")), true, sdk.SandboxFullAccess, false, sdk.ErrDocDenied},
		{"strict: 附件目录放行", abs(filepath.Join(home, "attachments", "pic.png")), true, sdk.SandboxFullAccess, true, nil},
		{"空路径", "   ", false, sdk.SandboxFullAccess, false, sdk.ErrDocDenied},
		{"NUL 字节", "note\x00.md", false, sdk.SandboxFullAccess, false, sdk.ErrDocDenied},
		{"目录", "sub", false, sdk.SandboxFullAccess, false, sdk.ErrDocDenied},
		{"不存在", "missing.md", false, sdk.SandboxFullAccess, false, sdk.ErrDocNotFound},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sb := &fakeSandbox{mode: c.mode, root: ws}
			r := NewResolver(sb, home)
			got, err := r.Resolve(c.path, c.strict)
			if c.wantOK {
				if err != nil {
					t.Fatalf("应放行,得错误: %v", err)
				}
				if !filepath.IsAbs(got) {
					t.Fatalf("应返回绝对路径,得 %q", got)
				}
				return
			}
			if err == nil {
				t.Fatalf("应拒绝,却放行: %s", got)
			}
			if c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Fatalf("错误类型应为 %v,得 %v", c.wantErr, err)
			}
		})
	}
}

// 无沙箱(TUI/CLI 单机)时绝对路径放行,相对路径锚定 cwd。
func TestResolveWithoutSandbox(t *testing.T) {
	_, outside, home := newFixture(t)
	r := NewResolver(nil, home)
	if _, err := r.Resolve(filepath.Join(outside, "secret.txt"), false); err != nil {
		t.Fatalf("无沙箱应放行绝对路径: %v", err)
	}
	if _, err := r.Resolve(filepath.Join(outside, "secret.txt"), true); err == nil {
		t.Fatal("strict 无沙箱仍应收窄根集合")
	}
}

// 附件路径回退(2026-09-17 真机):模型拿到的路径可能少一层「附件根」,或直接用
// 前端标识 `/attachments/<rel>`。两种都必须能解析 —— 否则用户看到的是
// 「docview: 文件不存在: 20260916-213605」这种把目录名当文件名的报错。
func TestResolveAttachmentFallback(t *testing.T) {
	_, outside, home := newFixture(t)
	rel := filepath.Join("20260916-213605", "report.pdf")
	want := filepath.Join(home, "attachments", rel)
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(nil, home)
	// 期望值过一遍 EvalSymlinks:macOS 上 /var → /private/var,解析结果必是真实路径
	// (解析器本身就做这一步,测试不能拿未归一的路径去比)。
	realWant, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}

	// ① 相对附件根(模型把目录名当路径那种写法)
	got, err := r.Resolve(filepath.ToSlash(rel), true)
	if err != nil {
		t.Fatalf("相对附件根形式应能解析: %v", err)
	}
	if got != realWant {
		t.Fatalf("解析结果 = %q,想要 %q", got, realWant)
	}
	// ② 前端标识形式
	got, err = r.Resolve("/attachments/"+filepath.ToSlash(rel), true)
	if err != nil {
		t.Fatalf("/attachments/<rel> 形式应能解析: %v", err)
	}
	if got != realWant {
		t.Fatalf("解析结果 = %q,想要 %q", got, realWant)
	}
	// ③ 绝对路径不抽奖:workspace 外仍被拒
	if _, err := r.Resolve(filepath.Join(outside, "secret.txt"), true); !errors.Is(err, sdk.ErrDocDenied) {
		t.Fatalf("绝对路径越界应仍被拒,得到 %v", err)
	}
	// ④ 真的不存在时,报错里的路径仍是用户给的那串(便于对照)
	_, err = r.Resolve("20990101-000000/nope.pdf", true)
	if !errors.Is(err, sdk.ErrDocNotFound) || !strings.Contains(err.Error(), "20990101-000000/nope.pdf") {
		t.Fatalf("不存在的文件应报 ErrDocNotFound 且保留原路径,得到 %v", err)
	}
}

// within 前缀匹配不得把 /root2 误判为 /root 内。
func TestWithinPrefix(t *testing.T) {
	// within 用 filepath.Separator 拼前缀,故域值也按平台构造:Windows 上分隔符是 \,
	// 硬编码 "/a/root/x" 会因前缀不匹配而误判「不在内」。
	root := filepath.FromSlash("/a/root")
	if within(root, filepath.FromSlash("/a/root2/x")) {
		t.Fatal("同前缀不同目录段不得判为在内")
	}
	if !within(root, filepath.FromSlash("/a/root/x")) {
		t.Fatal("子路径应判为在内")
	}
	if !within(root, root) {
		t.Fatal("自身应判为在内")
	}
}
