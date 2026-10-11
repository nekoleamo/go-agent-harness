package toolimage

import (
	"context"
	"encoding/json"
	"image"
	"image/png"
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

// 工具层用例:七动作的调度、路径裁决、批量、覆盖语义、宿主 codec seam。

// buildEnv 装配 host-tools + tool-image(带指定沙箱档)。
func buildEnv(t *testing.T, root string, mode sdk.SandboxMode) sdk.Ctx {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	sb := policyguard.DefaultSandbox(root)
	sb.SetMode(mode)
	if err := c.Provide("ctx.sandbox", sb); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	return c
}

func call(t *testing.T, c sdk.Ctx, args string) map[string]any {
	t.Helper()
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "image_ops", args)
	if err != nil {
		t.Fatalf("image_ops 调用失败: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("结果应为 JSON: %s(%v)", res.Content, err)
	}
	return out
}

// firstResult 取第一条逐条结果。
func firstResult(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	rs, ok := out["results"].([]any)
	if !ok || len(rs) == 0 {
		t.Fatalf("应有逐条结果,得 %v", out)
	}
	return rs[0].(map[string]any)
}

func needErr(t *testing.T, m map[string]any, want string) {
	t.Helper()
	e, ok := m["error"].(string)
	if !ok {
		t.Fatalf("应返回 error,得 %v", m)
	}
	if want != "" && !strings.Contains(e, want) {
		t.Fatalf("错误文案应含 %q,得 %q", want, e)
	}
}

func TestImageOpsActions(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	src := filepath.Join(ws, "a.png")
	mustWritePNG(t, src, solidImage(60, 40))

	// info
	out := call(t, c, `{"action":"info","src":"a.png"}`)
	r := firstResult(t, out)
	if r["format"] != "png" || r["width"].(float64) != 60 {
		t.Fatalf("info 应报格式与尺寸: %v", r)
	}
	if _, ok := out["caps"]; !ok {
		t.Fatal("info 应回传能力面(模型据此知道能转什么)")
	}

	// convert:dst 缺省写到同目录
	out = call(t, c, `{"action":"convert","src":"a.png","format":"jpeg"}`)
	r = firstResult(t, out)
	if _, err := os.Stat(filepath.Join(ws, "a_convert.jpg")); err != nil {
		t.Fatalf("convert 应写出 a_convert.jpg: %v(%v)", err, r)
	}
	// 再来一次 → 目标已存在,默认不覆盖
	out = call(t, c, `{"action":"convert","src":"a.png","format":"jpeg"}`)
	needErr(t, firstResult(t, out), "目标已存在")
	// overwrite 才覆盖
	out = call(t, c, `{"action":"convert","src":"a.png","format":"jpeg","overwrite":true}`)
	if _, ok := firstResult(t, out)["error"]; ok {
		t.Fatalf("overwrite:true 应允许覆盖: %v", firstResult(t, out))
	}

	// resize
	out = call(t, c, `{"action":"resize","src":"a.png","width":30,"height":30}`)
	if !strings.Contains(firstResult(t, out)["output_size"].(string), "30x20") {
		t.Fatalf("resize 结果尺寸不对: %v", firstResult(t, out))
	}
	// rotate
	out = call(t, c, `{"action":"rotate","src":"a.png","rotate":90}`)
	if !strings.Contains(firstResult(t, out)["output_size"].(string), "40x60") {
		t.Fatalf("rotate 结果尺寸不对: %v", firstResult(t, out))
	}
	// crop
	out = call(t, c, `{"action":"crop","src":"a.png","cw":10,"ch":10}`)
	if !strings.Contains(firstResult(t, out)["output_size"].(string), "10x10") {
		t.Fatalf("crop 结果尺寸不对: %v", firstResult(t, out))
	}
	// thumbnail 缺省长边 320
	out = call(t, c, `{"action":"thumbnail","src":"a.png"}`)
	if !strings.Contains(firstResult(t, out)["output_size"].(string), "60x40") {
		t.Fatalf("小图缩略图不该被放大: %v", firstResult(t, out))
	}
	// compress
	out = call(t, c, `{"action":"compress","src":"a.png","target_kb":2}`)
	if _, ok := firstResult(t, out)["dst_bytes"]; !ok {
		t.Fatalf("compress 应报出体积: %v", firstResult(t, out))
	}
	// 缺参数要给路
	for _, bad := range []string{
		`{"action":"convert","src":"a.png"}`,            // 缺 format
		`{"action":"resize","src":"a.png"}`,             // 缺尺寸
		`{"action":"crop","src":"a.png"}`,               // 缺裁剪宽高
		`{"action":"rotate","src":"a.png","rotate":45}`, // 非法角度
		`{"action":"compress","src":"a.png"}`,           // 缺目标体积
		`{"action":"nope","src":"a.png"}`,               // 未知动作
	} {
		if _, ok := firstResult(t, call(t, c, bad))["error"]; !ok {
			t.Fatalf("%s 应报错并给出路", bad)
		}
	}
	// 缺 src 是顶层错误(连逐条都谈不上)
	needErr(t, call(t, c, `{"action":"info"}`), "src")
}

func TestImageOpsBatchAndDstRule(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	for _, n := range []string{"a.png", "b.png", "中文 名.png"} {
		mustWritePNG(t, filepath.Join(ws, n), solidImage(8, 8))
	}
	out := call(t, c, `{"action":"convert","srcs":["a.png","b.png","中文 名.png"],"format":"png"}`)
	if out["count"] != float64(3) || out["failed"] != nil {
		t.Fatalf("三张都该成功: %v", out)
	}
	for i, want := range []string{"a_convert.png", "b_convert.png", "中文 名_convert.png"} {
		if _, err := os.Stat(filepath.Join(ws, want)); err != nil {
			t.Fatalf("第 %d 张应落盘 %s: %v(结果 %v)", i, want, err, out)
		}
	}
	// 批量 + 指定单个 dst = 明确拒绝(否则三张互相覆盖)
	out = call(t, c, `{"action":"convert","srcs":["a.png","b.png"],"format":"png","dst":"x.png"}`)
	if _, ok := out["error"]; !ok {
		t.Fatalf("批量+dst 应拒绝: %v", out)
	}
	// 批量里一张不存在:逐条报,成功的仍生效(换个动作,免得撞上一批的产物名)
	out = call(t, c, `{"action":"thumbnail","srcs":["a.png","missing.png"]}`)
	if out["failed"].(float64) != 1 {
		t.Fatalf("应报出 1 条失败: %v", out)
	}
}

// TestImageOpsPathJudgment 写目标裁决不可漏:输出路径(dst)也必须过沙箱。
func TestImageOpsPathJudgment(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxWorkspace)
	mustWritePNG(t, filepath.Join(ws, "a.png"), solidImage(8, 8))
	if err := os.WriteFile(filepath.Join(outside, "secret.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outAbs := filepath.ToSlash(outside)
	// dst 越界
	needErr(t, firstResult(t, call(t, c, `{"action":"convert","src":"a.png","format":"png","dst":"`+outAbs+`/x.png"}`)), "workspace")
	// src 越界(读也限区内)
	needErr(t, firstResult(t, call(t, c, `{"action":"info","src":"`+outAbs+`/secret.png"}`)), "workspace")
	// read-only 档下区内写也要拒
	ro := buildEnv(t, ws, sdk.SandboxReadOnly)
	needErr(t, firstResult(t, call(t, ro, `{"action":"convert","src":"a.png","format":"png"}`)), "")
}

func TestDefinitionDeclaresPathParamsAndLimits(t *testing.T) {
	d := (&ImageTool{}).Definition()
	if d.Name != "image_ops" {
		t.Fatalf("工具名 = %q", d.Name)
	}
	// K1–K4 必须在描述里(模型据此先摆正/注意上限/知道 GPS 被剥离)
	for _, kw := range []string{"Orientation", "3000 万像素", "首帧", "GPS"} {
		if !strings.Contains(d.Description, kw) {
			t.Fatalf("描述缺 %q(K1–K4 要求写进描述):\n%s", kw, d.Description)
		}
	}
	// 读写分开声明,输出路径必须有
	got := map[string]sdk.PathAccess{}
	for _, p := range d.PathParams {
		got[p.Arg] = p.Access
	}
	if got["src"] != sdk.PathRead {
		t.Fatalf("src 应声明为读,得 %q", got["src"])
	}
	if got["dst"] != sdk.PathWrite {
		t.Fatalf("dst 应声明为写(漏登记 = 写目标裁决形同虚设),得 %q", got["dst"])
	}
}

// TestHostCodecWins 宿主注册的原生实现优先(Android 侧 seam)。
func TestHostCodecWins(t *testing.T) {
	stub := &stubCodec{}
	sdk.RegisterImageCodec(stub)
	defer sdk.RegisterImageCodec(nil)
	if pickCodec(nil) != sdk.ImageCodec(stub) {
		t.Fatal("宿主注册的实现应被选中")
	}
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	out := call(t, c, `{"action":"info","src":"a.png"}`)
	if out["codec"] != "host" {
		t.Fatalf("codec 应报 host,得 %v", out["codec"])
	}
	if r := firstResult(t, out); r["format"] != "heic" {
		t.Fatalf("应走宿主实现的结果: %v", r)
	}
	// 注销后回内置
	sdk.RegisterImageCodec(nil)
	if _, ok := pickCodec(nil).(*GoCodec); !ok {
		t.Fatal("注销后应回内置纯 Go 实现")
	}
}

// stubCodec 假宿主实现(只报自己认识 heic)。
type stubCodec struct{}

func (s *stubCodec) Caps() sdk.CodecCaps {
	return sdk.CodecCaps{Decode: []sdk.ImageFormat{sdk.FormatHEIC}, Encode: []sdk.ImageFormat{sdk.FormatWEBPLossy},
		MaxPixels: 200_000_000, CanDownscale: true}
}

func (s *stubCodec) Info(path string) (sdk.ImageInfo, error) {
	return sdk.ImageInfo{Path: path, Format: sdk.FormatHEIC, Width: 4032, Height: 3024}, nil
}

func (s *stubCodec) Transform(src, dst string, spec sdk.TransformSpec) error {
	// 造一张真 PNG 顶上(宿主实现的行为不必与内置一致)
	fh, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer fh.Close()
	return png.Encode(fh, image.NewRGBA(image.Rect(0, 0, 10, 10)))
}

var _ = context.Background

// TestResizeOneSidedActuallyResizes 只给一边的 resize 必须真的缩放。
// 上一版只认"宽高都给":`resize width=800` 落进 else 分支、MaxDim=0 ⇒ **什么都不做却报成功**,
// 调用方拿到原尺寸还以为是缩过的(静默忽略参数比报错危险得多)。
func TestResizeOneSidedActuallyResizes(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	mustWritePNG(t, filepath.Join(ws, "a.png"), solidImage(200, 100))

	out := call(t, c, `{"action":"resize","src":"a.png","width":50,"dst":"w.png"}`)
	if r := firstResult(t, out); r["output_size"] != "50x25" {
		t.Fatalf("只给 width 应按比例得 50x25,得 %v", r["output_size"])
	}
	out = call(t, c, `{"action":"resize","src":"a.png","height":50,"dst":"h.png"}`)
	if r := firstResult(t, out); r["output_size"] != "100x50" {
		t.Fatalf("只给 height 应按比例得 100x50,得 %v", r["output_size"])
	}
	// exact 单边是明确的错(强制尺寸没有第二边可依)
	out = call(t, c, `{"action":"resize","src":"a.png","width":50,"mode":"exact","dst":"e.png"}`)
	needErr(t, firstResult(t, out), "exact")
	// 两种尺寸口径同时给要明确拒(否则取哪个是暗规则)
	out = call(t, c, `{"action":"resize","src":"a.png","width":50,"height":50,"max_dim":100,"dst":"m.png"}`)
	needErr(t, firstResult(t, out), "互斥")
}
