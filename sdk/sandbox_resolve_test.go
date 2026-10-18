package sdk

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// ResolveSandboxPath 是**写目标裁决的唯一实现**(所有路径类工具共用):
// 覆盖三档 × 相对/绝对 × 各可选能力的优先序。少一个分支就是某个工具漏裁决的入口。

// baseSB 最老形态的沙箱:**只实现 Sandbox** —— 读校验与联动能力都没有,
// 正好用来压 ResolveSandboxPath 的兜底分支(按有效档位判读)。
type baseSB struct {
	root string
	mode SandboxMode
	// 记录收到的路径:用来断言"确实把裁决交给了沙箱"
	sawPath string
	sawRoot string
}

func (f *baseSB) Mode() SandboxMode           { return f.mode }
func (f *baseSB) SetMode(m SandboxMode)       { f.mode = m }
func (f *baseSB) Root() string                { return f.root }
func (f *baseSB) ValidatePath(p string) error { f.sawPath = p; return nil }

// fakeSB 追加读校验能力(ReadValidator)。
type fakeSB struct{ baseSB }

func (f *fakeSB) ValidateRead(p string) error { f.sawPath = p; return nil }

// rootScopedSB RootScoped:按显式工作根裁决(隔离子代理形态)。
type rootScopedSB struct{ baseSB }

func (f *rootScopedSB) ValidatePathAt(root, p string) error {
	f.sawRoot, f.sawPath = root, p
	return nil
}
func (f *rootScopedSB) ValidateReadAt(root, p string) error {
	f.sawRoot, f.sawPath = root, p
	return nil
}

// rootScopedReadSB 同时实现 RootScoped 与 ReadValidator(宿主沙箱的完整形态:
// 两个都在时,读优先走 ValidateReadAt —— 两者都按**同一工作根**裁决才不会出现双重标准)。
type rootScopedReadSB struct{ baseSB }

func (f *rootScopedReadSB) ValidatePathAt(root, p string) error {
	f.sawRoot, f.sawPath = root, p
	return nil
}
func (f *rootScopedReadSB) ValidateReadAt(root, p string) error {
	f.sawRoot, f.sawPath = root, p
	return nil
}
func (f *rootScopedReadSB) ValidateRead(p string) error { f.sawPath = p; return nil }

// forCtxSB PathValidatorFor/ReadValidatorFor:按会话裁决(多页签形态)。
type forCtxSB struct{ baseSB }

func (f *forCtxSB) ValidatePathFor(_ context.Context, p string) error { f.sawPath = p; return nil }
func (f *forCtxSB) ValidateReadFor(_ context.Context, p string) error { f.sawPath = p; return nil }

// effSB 只实现无参 EffectiveSandbox(单会话形态)。
type effSB struct{ baseSB }

func (f *effSB) EffectiveMode() SandboxMode { return f.baseSB.mode }

// effForSB 只实现带 ctx 的 EffectiveSandboxFor(多页签形态)。
type effForSB struct{ baseSB }

func (f *effForSB) EffectiveModeFor(context.Context) SandboxMode { return f.baseSB.mode }

func TestResolveSandboxPathNoSandbox(t *testing.T) {
	// 未装配沙箱(单测/独立部署/外部进程):只解析不限制
	got, err := ResolveSandboxPath(context.Background(), nil, "a/b.txt", true)
	if err != nil || got != filepath.Clean("a/b.txt") {
		t.Fatalf("无沙箱应放行并 Clean: %q %v", got, err)
	}
}

func TestResolveSandboxPathRelativeBase(t *testing.T) {
	// 相对路径基准 = hint.Root(优先)而不是沙箱根
	sb := &rootScopedSB{baseSB{root: "/ws", mode: SandboxWorkspace}}
	ctx := WithSandboxHint(context.Background(), SandboxHint{Mode: SandboxWorkspace, Root: "/iso"})
	got, err := ResolveSandboxPath(ctx, sb, "sub/a.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/iso", "sub", "a.txt"); got != want {
		t.Fatalf("相对路径应按本次工作根解析: got %q want %q", got, want)
	}
	if sb.sawRoot != "/iso" {
		t.Fatalf("RootScoped 应收到 hint.Root 作裁决根,got %q", sb.sawRoot)
	}
	// 无 hint 时退回沙箱根
	got, err = ResolveSandboxPath(context.Background(), sb, "a.txt", true)
	if err != nil || got != filepath.Join("/ws", "a.txt") {
		t.Fatalf("无 hint 应退回沙箱根: %q %v", got, err)
	}
	// 绝对路径原样(只 Clean)
	abs := filepath.Join(t.TempDir(), "x.txt")
	if got, err = ResolveSandboxPath(context.Background(), sb, abs, true); err != nil || got != abs {
		t.Fatalf("绝对路径应原样: %q %v", got, err)
	}
}

func TestResolveSandboxPathCapabilityPriority(t *testing.T) {
	// 同时实现 RootScoped + PathValidatorFor + ReadValidator:RootScoped 优先
	rs := &rootScopedSB{baseSB{root: "/ws", mode: SandboxWorkspace}}
	if _, err := ResolveSandboxPath(context.Background(), rs, "/ws/a", true); err != nil || rs.sawPath == "" {
		t.Fatalf("写应走 RootScoped.ValidatePathAt: %v", err)
	}
	rs2 := &rootScopedReadSB{baseSB{root: "/ws", mode: SandboxWorkspace}}
	if _, err := ResolveSandboxPath(context.Background(), rs2, "/ws/a", false); err != nil || rs2.sawPath == "" {
		t.Fatalf("读应走 RootScoped.ValidateReadAt: %v", err)
	}
	// 只有 PathValidatorFor(无 RootScoped)
	fc := &forCtxSB{baseSB{root: "/ws", mode: SandboxWorkspace}}
	if _, err := ResolveSandboxPath(context.Background(), fc, "a.txt", true); err != nil || fc.sawPath == "" {
		t.Fatalf("写应走 ValidatePathFor: %v", err)
	}
	// 只有 ReadValidator
	ro := &fakeSB{baseSB{root: "/ws", mode: SandboxWorkspace}}
	if _, err := ResolveSandboxPath(context.Background(), ro, "a.txt", false); err != nil || ro.sawPath == "" {
		t.Fatalf("读应走 ValidateRead: %v", err)
	}
	// 只有 Sandbox.ValidatePath(最老形态)
	plain := &baseSB{root: "/ws", mode: SandboxWorkspace}
	if _, err := ResolveSandboxPath(context.Background(), plain, "a.txt", true); err != nil || plain.sawPath == "" {
		t.Fatalf("写应走 ValidatePath: %v", err)
	}
}

func TestResolveSandboxPathReadFallbackByMode(t *testing.T) {
	// 未实现读校验能力时按**有效档位**兜底:full/read-only 放行,workspace 限区内
	// (这条兜底就是"未装配 ReadValidator 的旧沙箱"不会退化成无限制的地方)
	//
	// 根与区外路径一律由 t.TempDir() 派生,不用 POSIX 字面量:Windows 上 `/ws`、`/etc/passwd`
	// **没有盘符就不算绝对路径**(filepath.IsAbs=false)⇒ 会被当成本地相对路径拼进根,
	// 区外路径于是变成"区内",这条兜底分支根本没被验到(2026-10-11 起 test-windows 一直红在
	// 这个假想上;本地 macOS 绿不代表它绿)。
	root := filepath.Join(t.TempDir(), "ws")
	outside := filepath.Join(t.TempDir(), "passwd")
	for _, tc := range []struct {
		mode    SandboxMode
		wantErr bool
	}{
		{SandboxFullAccess, false},
		{SandboxReadOnly, false},
		{SandboxWorkspace, true},
	} {
		sb := &baseSB{root: root, mode: tc.mode}
		_, err := ResolveSandboxPath(context.Background(), sb, outside, false)
		if (err != nil) != tc.wantErr {
			t.Fatalf("mode=%s 读区外:err=%v, wantErr=%v", tc.mode, err, tc.wantErr)
		}
		// 区内一律放行
		if _, err := ResolveSandboxPath(context.Background(), sb, filepath.Join(root, "a.txt"), false); err != nil {
			t.Fatalf("mode=%s 读区内不该被拒: %v", tc.mode, err)
		}
	}
	// 根本身(无尾分隔符)也算区内
	sb := &baseSB{root: root, mode: SandboxWorkspace}
	if _, err := ResolveSandboxPath(context.Background(), sb, root, false); err != nil {
		t.Fatalf("root 自身应放行: %v", err)
	}
	// 空 root 的极端情形不能 panic(单测里 sandbox.Root() 可能为空)
	sbEmpty := &baseSB{root: "", mode: SandboxWorkspace}
	if _, err := ResolveSandboxPath(context.Background(), sbEmpty, "a.txt", false); err == nil {
		t.Fatal("空 root + workspace-write 不该放行区外相对路径")
	}
}

func TestResolveSandboxPathPropagatesRejection(t *testing.T) {
	// 沙箱拒绝的错误必须原样传出(工具据此给模型"被拦"的文案)
	denied := &denySB{baseSB{root: "/ws", mode: SandboxWorkspace}}
	_, err := ResolveSandboxPath(context.Background(), denied, "/etc/x", true)
	if err == nil || !strings.Contains(err.Error(), "拒绝") {
		t.Fatalf("沙箱拒绝应原样传出: %v", err)
	}
	deniedRead := &denyReadSB{baseSB{root: "/ws", mode: SandboxWorkspace}}
	if _, err := ResolveSandboxPath(context.Background(), deniedRead, "/etc/x", false); err == nil {
		t.Fatal("读拒绝应原样传出")
	}
}

type denySB struct{ baseSB }

func (d *denySB) ValidatePath(string) error { return errors.New("sandbox: 拒绝写该路径") }

type denyReadSB struct{ baseSB }

func (d *denyReadSB) ValidateRead(string) error { return errors.New("sandbox: 拒绝读该路径") }

func TestEffectiveSandboxModeFor(t *testing.T) {
	// 无任何联动能力 → 用 Mode()
	if got := EffectiveSandboxModeFor(context.Background(), &baseSB{mode: SandboxReadOnly}); got != SandboxReadOnly {
		t.Fatalf("无联动能力应回落 Mode(),got %q", got)
	}
	// EffectiveSandbox(无参)
	// EffectiveSandbox(无参):声明的档与 Mode() 不同 ⇒ 必须取声明值
	e := &effSB{baseSB{mode: SandboxReadOnly}}
	if got := EffectiveSandboxModeFor(context.Background(), e); got != SandboxReadOnly {
		t.Fatalf("应取 EffectiveMode,got %q", got)
	}
	// EffectiveSandboxFor(带 ctx)优先于无参版
	e2 := &effForSB{baseSB{mode: SandboxWorkspace}}
	if got := EffectiveSandboxModeFor(context.Background(), e2); got != SandboxWorkspace {
		t.Fatalf("应优先 EffectiveModeFor,got %q", got)
	}
}
