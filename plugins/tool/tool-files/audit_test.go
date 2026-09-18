// S-P1-1 文件改动审计单测:工具写盘前后自取内容 → file/change 事件(供 /diff 与三端审查面)。
// 这里用同包假账本(只依赖 sdk),不装配真实会话插件 —— 审计是旁路,写盘结果不受其影响。
package toolfiles

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/plugins/policy/policy-guard"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeLog 只记账本追加/回放两个方法,其余按空实现(测试不需要投影/落盘)。
type fakeLog struct{ evs []sdk.SessionEvent }

func (f *fakeLog) Append(ev sdk.SessionEvent) error {
	f.evs = append(f.evs, ev)
	return nil
}
func (f *fakeLog) DeriveMessages() []sdk.LLMMessage              { return nil }
func (f *fakeLog) Replay() []sdk.SessionEvent                    { return f.evs }
func (f *fakeLog) Flush() error                                  { return nil }
func (f *fakeLog) SetPath(string)                                {}
func (f *fakeLog) Load(string) error                             { return nil }
func (f *fakeLog) SetHistory(int)                                {}
func (f *fakeLog) RegisterCompressor(int, sdk.SessionCompressor) {}

// changes 取出已记录的 file/change 事件载荷。
func (f *fakeLog) changes(t *testing.T) []sdk.FileChangeEvent {
	t.Helper()
	var out []sdk.FileChangeEvent
	for _, ev := range f.evs {
		if ev.Kind != sdk.EventFileChange {
			t.Fatalf("只应记录 file/change: %s", ev.Kind)
		}
		ev2, ok := sdk.FileChangeFrom(ev.Payload)
		if !ok {
			t.Fatalf("载荷应为 FileChangeEvent: %T", ev.Payload)
		}
		out = append(out, ev2)
	}
	return out
}

// buildAuditEnv 装配 host-tools + tool-files + 假会话账本。
func buildAuditEnv(t *testing.T, root string, mode sdk.SandboxMode) (sdk.Ctx, *fakeLog) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	sb := policyguard.DefaultSandbox(root)
	sb.SetMode(mode)
	if err := c.Provide("ctx.sandbox", sb); err != nil {
		t.Fatal(err)
	}
	fl := &fakeLog{}
	if err := c.Provide("ctx.sessions", fl); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	return c, fl
}

// TestFileChangeOnNewFile 新建文件:Created + 全部新增行 + Rel 相对工作区 + 可读 patch。
func TestFileChangeOnNewFile(t *testing.T) {
	c, fl := buildAuditEnv(t, t.TempDir(), sdk.SandboxWorkspace)
	if _, err := call(t, c, "file_write", `{"path":"sub/a.txt","content":"l1\nl2\nl3\n"}`); err != nil {
		t.Fatal(err)
	}
	cs := fl.changes(t)
	if len(cs) != 1 {
		t.Fatalf("应记录 1 条改动: %d", len(cs))
	}
	e := cs[0]
	if e.Op != "write" || e.Tool != "file_write" || !e.Created {
		t.Fatalf("语义字段: %+v", e)
	}
	if e.Rel != "sub/a.txt" {
		t.Fatalf("Rel 应为工作区相对路径(斜杠): %q", e.Rel)
	}
	if e.Added != 3 || e.Removed != 0 {
		t.Fatalf("+/−: +%d -%d", e.Added, e.Removed)
	}
	if !strings.HasPrefix(e.Diff, "@@") || !strings.Contains(e.Diff, "+l2") {
		t.Fatalf("patch 应可读: %q", e.Diff)
	}
}

// TestFileChangeOnEdit 编辑:同一文件两条事件(写入 + 编辑),编辑条只有真实增删。
func TestFileChangeOnEdit(t *testing.T) {
	c, fl := buildAuditEnv(t, t.TempDir(), sdk.SandboxWorkspace)
	if _, err := call(t, c, "file_write", `{"path":"x.txt","content":"a\nb\nc\n"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, c, "file_edit", `{"path":"x.txt","old":"b","new":"B1\nB2"}`); err != nil {
		t.Fatal(err)
	}
	cs := fl.changes(t)
	if len(cs) != 2 {
		t.Fatalf("应有两条改动事件: %d", len(cs))
	}
	e := cs[1]
	if e.Op != "edit" || e.Tool != "file_edit" || e.Created {
		t.Fatalf("编辑条语义: %+v", e)
	}
	if e.Added != 2 || e.Removed != 1 {
		t.Fatalf("编辑增删行数: +%d -%d", e.Added, e.Removed)
	}
	if !strings.Contains(e.Diff, "-b") || !strings.Contains(e.Diff, "+B1") {
		t.Fatalf("patch 内容: %q", e.Diff)
	}
}

// TestFileChangeAppend 追加:只记新增行。
func TestFileChangeAppend(t *testing.T) {
	c, fl := buildAuditEnv(t, t.TempDir(), sdk.SandboxWorkspace)
	call(t, c, "file_write", `{"path":"a.txt","content":"1\n"}`)
	if _, err := call(t, c, "file_append", `{"path":"a.txt","content":"2\n3\n"}`); err != nil {
		t.Fatal(err)
	}
	cs := fl.changes(t)
	if len(cs) != 2 || cs[1].Op != "append" {
		t.Fatalf("追加应记第二条: %+v", cs)
	}
	if cs[1].Added != 2 || cs[1].Removed != 0 {
		t.Fatalf("追加增删: +%d -%d", cs[1].Added, cs[1].Removed)
	}
}

// TestFileChangeSkippedWhenContentUnchanged 内容未变不刷空事件(避免审查面噪声)。
func TestFileChangeSkippedWhenContentUnchanged(t *testing.T) {
	c, fl := buildAuditEnv(t, t.TempDir(), sdk.SandboxWorkspace)
	call(t, c, "file_write", `{"path":"s.txt","content":"same\n"}`)
	call(t, c, "file_write", `{"path":"s.txt","content":"same\n"}`)
	if cs := fl.changes(t); len(cs) != 1 {
		t.Fatalf("内容未变不应追加事件: %d", len(cs))
	}
}

// TestFileChangeNoWriteNoEvent 失败路径(只读档拒绝写)不得留下审计事件。
func TestFileChangeNoWriteNoEvent(t *testing.T) {
	c, fl := buildAuditEnv(t, t.TempDir(), sdk.SandboxReadOnly)
	out, _ := call(t, c, "file_write", `{"path":"x.txt","content":"x"}`)
	if out["error"] == nil {
		t.Fatalf("只读档应拒绝写: %v", out)
	}
	if cs := fl.changes(t); len(cs) != 0 {
		t.Fatalf("写失败不应记录改动: %+v", cs)
	}
}

// TestFileChangeBinaryOnlyStats 二进制目标:只记统计,不给逐行 diff(不乱码当 patch)。
func TestFileChangeBinaryOnlyStats(t *testing.T) {
	c, fl := buildAuditEnv(t, t.TempDir(), sdk.SandboxWorkspace)
	if _, err := call(t, c, "file_write", `{"path":"b.bin","content":"\u0000\u0001binary"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, c, "file_append", `{"path":"b.bin","content":"more"}`); err != nil {
		t.Fatal(err)
	}
	cs := fl.changes(t)
	last := cs[len(cs)-1]
	if !last.Binary {
		t.Fatalf("二进制应标记: %+v", last)
	}
	if last.Diff != "" || last.Added != 0 || last.Removed != 0 {
		t.Fatalf("二进制不应给行级 diff: %+v", last)
	}
}

// TestFileChangeTruncatedPatch 超大改动:patch 按预算截断且显式标记(不静默丢内容)。
func TestFileChangeTruncatedPatch(t *testing.T) {
	c, fl := buildAuditEnv(t, t.TempDir(), sdk.SandboxWorkspace)
	var sb strings.Builder
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&sb, "line-%04d-abcdefghijklmnopqrstuvwxyz\n", i)
	}
	body, _ := json.Marshal(map[string]string{"path": "big.txt", "content": sb.String()})
	if _, err := call(t, c, "file_write", string(body)); err != nil {
		t.Fatal(err)
	}
	cs := fl.changes(t)
	e := cs[0]
	if !e.Truncated {
		t.Fatalf("超预算 patch 应标记截断: %d 字节", len(e.Diff))
	}
	if len(e.Diff) > sdk.FileChangeMaxDiffBytes+64 {
		t.Fatalf("截断后仍超预算: %d", len(e.Diff))
	}
	if e.Added != 4000 || e.Removed != 0 {
		t.Fatalf("计数必须真实(截断只影响 patch): +%d -%d", e.Added, e.Removed)
	}
	if !strings.Contains(e.Diff, "已截断") {
		t.Fatalf("截断提示缺失: %q", e.Diff[len(e.Diff)-60:])
	}
}

// TestFileChangeWithoutSessions 未装配 ctx.sessions(外部进程/极简 profile):写盘照常,不 panic。
func TestFileChangeWithoutSessions(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxWorkspace) // 不提供 ctx.sessions
	out, err := call(t, c, "file_write", `{"path":"x.txt","content":"ok"}`)
	if err != nil || out["error"] != nil {
		t.Fatalf("无账本时写盘应照常: %v %v", err, out)
	}
}

// —— S-P1-1 外部化补齐:外部进程没有宿主 Ctx,改动经 rec(桥回传)交宿主落账 ——

// stubRecorder 假回传出口(记录收到的 file/change 事件,可注入失败)。
type stubRecorder struct {
	evs  []sdk.FileChangeEvent
	fail bool
}

func (r *stubRecorder) RecordChange(ev sdk.FileChangeEvent) error {
	if r.fail {
		return fmt.Errorf("桥断了")
	}
	r.evs = append(r.evs, ev)
	return nil
}

// TestExternalRecorderPath 外部化路径(NewToolsWith):无宿主 Ctx 时靠 rec 回传;
// 回传的事件口径(统计/diff/Rel 留空由宿主补)必须与内嵌路径一致。
func TestExternalRecorderPath(t *testing.T) {
	dir := t.TempDir()
	rec := &stubRecorder{}
	tools := map[string]sdk.Tool{}
	for name, tl := range NewToolsWith(rec) { // 不经插件 Start = 无宿主 Ctx(等价外部进程)
		tools[name] = tl
	}
	tgt := filepath.Join(dir, "a.txt")
	out, err := tools["file_write"].Execute(context.Background(),
		marshalArgs(t, map[string]any{"path": tgt, "content": "hello\nworld\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if res, ok := out.(map[string]any); ok && res["error"] != nil {
		t.Fatalf("写盘应成功: %v", res["error"])
	}
	if len(rec.evs) != 1 {
		t.Fatalf("应回传一条 file/change,得到 %d 条", len(rec.evs))
	}
	ev := rec.evs[0]
	if ev.Path != tgt || ev.Op != "write" || ev.Tool != "file_write" {
		t.Fatalf("事件基本字段不符: %+v", ev)
	}
	if ev.Rel != "" {
		t.Fatalf("外部进程取不到沙箱根,Rel 应留空交给宿主补算: %q", ev.Rel)
	}
	if ev.Created != true || ev.Added != 2 || ev.Removed != 0 {
		t.Fatalf("新建文件统计不符: %+v", ev)
	}
	if !strings.Contains(ev.Diff, "+hello") {
		t.Fatalf("patch 应为可读 unified diff: %q", ev.Diff)
	}
	// 内容未变的重写:不刷空事件
	if _, err := tools["file_write"].Execute(context.Background(),
		marshalArgs(t, map[string]any{"path": tgt, "content": "hello\nworld\n"})); err != nil {
		t.Fatal(err)
	}
	if len(rec.evs) != 1 {
		t.Fatalf("同内容重写不应再记事件: %d 条", len(rec.evs))
	}
	// 回传失败不影响写盘结果(改动的文件已落盘)
	rec.fail = true
	tgt2 := filepath.Join(dir, "b.txt")
	if _, err := tools["file_write"].Execute(context.Background(),
		marshalArgs(t, map[string]any{"path": tgt2, "content": "x"})); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(tgt2); statErr != nil {
		t.Fatalf("回传失败不得影响写盘: %v", statErr)
	}
}

// TestExternalRecorderNil 无回传通道(裸进程直跑)时:写盘照常,不 panic。
func TestExternalRecorderNil(t *testing.T) {
	tools := NewTools() // rec 与宿主 Ctx 皆无
	tgt := filepath.Join(t.TempDir(), "c.txt")
	if _, err := tools["file_write"].Execute(context.Background(),
		marshalArgs(t, map[string]any{"path": tgt, "content": "y"})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tgt); err != nil {
		t.Fatalf("写盘照常: %v", err)
	}
}

// marshalArgs 工具参数 JSON(与既有用例同口径)。
func marshalArgs(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
