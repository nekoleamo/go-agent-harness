// 产物登记账本单测(MED-1):越界拒绝 / 大小上限 / 幂等 / 单次可用 / TTL / 目标授权 /
// 未实现 MediaSender 的通道显式报错 / 失败回滚可重试 / 文件变更后拒绝。
package im

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-session-log"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// mediaTransport 支持出站媒体的传输替身(记录投递 + 可编程失败)。
type mediaTransport struct {
	stubTransport
	sent   []MediaPayload
	fail   error
	routes []Route
}

func (m *mediaTransport) SendMedia(_ context.Context, to Route, p MediaPayload) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.sent = append(m.sent, p)
	m.routes = append(m.routes, to)
	return nil
}

// fakeSandbox 固定 root 的沙箱替身(工作区包含校验用)。
type fakeSandbox struct{ root string }

func (f fakeSandbox) Mode() sdk.SandboxMode     { return sdk.SandboxWorkspace }
func (f fakeSandbox) SetMode(sdk.SandboxMode)   {}
func (f fakeSandbox) Root() string              { return f.root }
func (f fakeSandbox) ValidatePath(string) error { return nil }

// buildMediaBridge 工作区 = root 的桥 + 媒体传输替身。
func buildMediaBridge(t *testing.T, tr Transport, workspace string, maxMB int) (*Bridge, sdk.Ctx) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if _, err := (&sessionlog.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.sandbox", fakeSandbox{root: workspace}); err != nil {
		t.Fatal(err)
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	b := New(c, &stubLoop{}, sessions, tr, Options{
		Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}, MediaMaxMB: maxMB,
	})
	return b, c
}

func TestRegisterArtifactGuards(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	tr := &mediaTransport{}
	b, _ := buildMediaBridge(t, tr, ws, 0) // 默认 20 MB
	var svc sdk.IMAttachmentService = b    // 契约自检

	inside := filepath.Join(ws, "报告.pdf")
	if err := os.WriteFile(inside, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	art, err := svc.RegisterArtifact(context.Background(), inside)
	if err != nil {
		t.Fatalf("工作区内文件应可登记: %v", err)
	}
	if art.ID == "" || art.Name != "报告.pdf" || art.Kind != "file" || art.Bytes != 8 {
		t.Fatalf("登记结果异常: %+v", art)
	}
	// 幂等:同文件同版本 → 同 id
	if again, err := svc.RegisterArtifact(context.Background(), inside); err != nil || again.ID != art.ID {
		t.Fatalf("重复登记应返回同 id: %+v err=%v", again, err)
	}
	// 越界(工作区外)拒绝
	o := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(o, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RegisterArtifact(context.Background(), o); err == nil || !strings.Contains(err.Error(), "工作区内") {
		t.Fatalf("工作区外文件应拒绝: %v", err)
	}
	// 目录 / 不存在 / 空路径
	if _, err := svc.RegisterArtifact(context.Background(), ws); err == nil {
		t.Fatal("目录不应可登记")
	}
	if _, err := svc.RegisterArtifact(context.Background(), filepath.Join(ws, "nope.txt")); err == nil {
		t.Fatal("不存在文件应报错")
	}
	if _, err := svc.RegisterArtifact(context.Background(), "  "); err == nil {
		t.Fatal("空路径应报错")
	}
	// 类型判定
	for name, want := range map[string]string{
		"a.png": "image", "b.mp4": "video", "c.silk": "voice", "d.zip": "file",
	} {
		p := filepath.Join(ws, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := svc.RegisterArtifact(context.Background(), p)
		if err != nil || got.Kind != want {
			t.Fatalf("%s 类型应为 %s: %+v err=%v", name, want, got, err)
		}
	}
}

func TestRegisterArtifactSizeLimit(t *testing.T) {
	ws := t.TempDir()
	tr := &mediaTransport{}
	b, _ := buildMediaBridge(t, tr, ws, 1) // 1 MB 上限
	var svc sdk.IMAttachmentService = b
	small := filepath.Join(ws, "small.png")
	big := filepath.Join(ws, "big.png")
	if err := os.WriteFile(small, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RegisterArtifact(context.Background(), small); err != nil {
		t.Fatalf("小文件应可登记: %v", err)
	}
	if _, err := svc.RegisterArtifact(context.Background(), big); err == nil || !strings.Contains(err.Error(), "过大") {
		t.Fatalf("超限文件应拒绝: %v", err)
	}
}

func TestSendArtifactFlow(t *testing.T) {
	ws := t.TempDir()
	tr := &mediaTransport{}
	b, _ := buildMediaBridge(t, tr, ws, 0)
	var svc sdk.IMAttachmentService = b
	var ctl sdk.IMControlService = b
	p := filepath.Join(ws, "chart.png")
	if err := os.WriteFile(p, []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	art, err := svc.RegisterArtifact(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if st := ctl.Status(); st.Artifacts != 1 {
		t.Fatalf("状态应显示 1 个待投产物: %+v", st)
	}
	// 未授权目标 → 拒绝(且条目保留)
	if err := svc.SendArtifact(context.Background(), "stranger", art.ID); err == nil || !strings.Contains(err.Error(), "未授权") {
		t.Fatalf("未授权目标应拒绝: %v", err)
	}
	if ids := b.artifactIDs(); len(ids) != 1 {
		t.Fatalf("拒绝后条目应保留: %v", ids)
	}
	// 未登记 id → 拒绝
	if err := svc.SendArtifact(context.Background(), "owner", "nope"); err == nil || !strings.Contains(err.Error(), "未登记") {
		t.Fatalf("未登记 id 应拒绝: %v", err)
	}
	// 正常投递:通道收到 payload(含 Kind/Name/Size)
	if err := svc.SendArtifact(context.Background(), "owner", art.ID); err != nil {
		t.Fatalf("应投递成功: %v", err)
	}
	if len(tr.sent) != 1 {
		t.Fatalf("通道应收到 1 条媒体: %+v", tr.sent)
	}
	got := tr.sent[0]
	if got.ArtifactID != art.ID || got.Kind != MediaImage || got.Name != "chart.png" || got.Size != 3 {
		t.Fatalf("payload 异常: %+v", got)
	}
	if tr.routes[0].ChatID != "owner" {
		t.Fatalf("路由异常: %+v", tr.routes[0])
	}
	// 单次可用:再发同 id → 未登记
	if err := svc.SendArtifact(context.Background(), "owner", art.ID); err == nil {
		t.Fatal("同一条目不应可重复投递")
	}
	if st := ctl.Status(); st.Artifacts != 0 {
		t.Fatalf("投递后应不再有待投产物: %+v", st)
	}
}

func TestSendArtifactFailureAndChange(t *testing.T) {
	ws := t.TempDir()
	tr := &mediaTransport{fail: context.DeadlineExceeded}
	b, _ := buildMediaBridge(t, tr, ws, 0)
	var svc sdk.IMAttachmentService = b
	p := filepath.Join(ws, "a.pdf")
	if err := os.WriteFile(p, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	art, err := svc.RegisterArtifact(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	// 通道失败 → 报错 + 条目回滚(可重试)
	if err := svc.SendArtifact(context.Background(), "owner", art.ID); err == nil || !strings.Contains(err.Error(), "投递失败") {
		t.Fatalf("通道失败应报错: %v", err)
	}
	if ids := b.artifactIDs(); len(ids) != 1 {
		t.Fatalf("失败后条目应回滚: %v", ids)
	}
	// 文件被改 → 拒绝并要求重新登记
	tr.fail = nil
	if err := os.WriteFile(p, []byte("v2-changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.SendArtifact(context.Background(), "owner", art.ID); err == nil || !strings.Contains(err.Error(), "已变更") {
		t.Fatalf("文件变更后应拒绝: %v", err)
	}
	// 重新登记后可正常发送
	art2, err := svc.RegisterArtifact(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if art2.ID == art.ID {
		t.Fatal("内容变更后 id 应不同")
	}
	if err := svc.SendArtifact(context.Background(), "owner", art2.ID); err != nil {
		t.Fatalf("重新登记后应可发送: %v", err)
	}
}

func TestArtifactTTLAndCapacity(t *testing.T) {
	book := newArtifactBook()
	now := time.Now()
	// TTL:过期条目取不到
	book.put(artifact{ID: "old", ExpiresAt: now.Add(-time.Second)})
	if _, ok := book.take("old", now); ok {
		t.Fatal("过期条目不应可取用")
	}
	// 容量:超出 8 条淘汰最旧
	for i := 0; i < artifactCap+3; i++ {
		book.put(artifact{ID: string(rune('a' + i)), ExpiresAt: now.Add(time.Minute)})
	}
	if n := book.count(); n != artifactCap {
		t.Fatalf("容量应为 %d,得 %d", artifactCap, n)
	}
	if _, ok := book.take("a", now); ok {
		t.Fatal("最旧条目应被淘汰")
	}
	// restore 后可重试
	a := artifact{ID: "x", ExpiresAt: now.Add(time.Minute)}
	book.put(a)
	if got, ok := book.take("x", now); !ok {
		t.Fatal("应可取用")
	} else {
		book.restore(got)
	}
	if _, ok := book.take("x", now); !ok {
		t.Fatal("restore 后应可再次取用")
	}
}

func TestSendArtifactUnsupportedChannel(t *testing.T) {
	ws := t.TempDir()
	plain := &stubTransport{} // 未实现 MediaSender
	b, _ := buildMediaBridge(t, plain, ws, 0)
	var svc sdk.IMAttachmentService = b
	p := filepath.Join(ws, "a.png")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	art, err := svc.RegisterArtifact(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SendArtifact(context.Background(), "owner", art.ID); err == nil ||
		!strings.Contains(err.Error(), "不支持出站文件") {
		t.Fatalf("未实现 MediaSender 的通道应显式报错: %v", err)
	}
}
