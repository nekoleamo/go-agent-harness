// Package toolimage 提供 tool-image 插件(F4):图像信息/转换/缩放/裁剪/旋转/压缩/缩略图。
//
// 编解码实现可换:默认内置纯 Go(桌面);宿主(Android 壳)注册 sdk.ImageCodec 后自动优先,
// 见 pickCodec。选路按**能力并集**:某格式只有宿主能处理就交给宿主,都不支持就**显式报错**
// 并说明需要什么 —— 不静默降级(方案 §M2/K3 与 D1)。
package toolimage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 tool-image。requires ctx.tools;ctx.sandbox 可选(未装配则不限);
// system.imageCodec 可选(宿主注册的原生编解码器)。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-image" }

// Start 注册 image_ops 工具。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	var sb sdk.Sandbox
	_ = c.Inject("ctx.sandbox", &sb)
	t := &ImageTool{sb: sb, c: c, codec: pickCodec(c)}
	return tools.Register(t), nil
}

// pickCodec 选路:ctx 上的宿主实现 > 宿主注册表 > 内置纯 Go。
// 三者都拿不到实现(不该发生)时返回内置,绝不返回 nil。
func pickCodec(c sdk.Ctx) sdk.ImageCodec {
	if c != nil {
		var hc sdk.ImageCodec
		if err := c.Inject("system.imageCodec", &hc); err == nil && hc != nil {
			return hc
		}
	}
	if hc := sdk.RegisteredImageCodec(); hc != nil {
		return hc
	}
	return NewGoCodec()
}

// ImageTool 单工具(image_ops):单 schema 多 action。
type ImageTool struct {
	sb    sdk.Sandbox
	c     sdk.Ctx
	codec sdk.ImageCodec
}

// imageOpsSchema 入参 schema(七动作共用;各动作只用其中几个字段)。
func imageOpsSchema() map[string]any {
	s := map[string]any{"type": "string"}
	i := map[string]any{"type": "integer"}
	b := map[string]any{"type": "boolean"}
	return map[string]any{
		"type":     "object",
		"required": []any{"action", "src"},
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []any{
				"info", "convert", "resize", "crop", "rotate", "compress", "thumbnail"}},
			"src":      s,                                           // 单张
			"srcs":     map[string]any{"type": "array", "items": s}, // 批量(与 src 二选一)
			"dst":      s,                                           // 缺省 = <src 同目录>/<同名>_<action>.<格式>
			"format":   map[string]any{"type": "string", "enum": []any{"jpeg", "png", "gif", "bmp", "tiff", "webp"}},
			"quality":  i, // 1..100(有损)
			"lossless": b,
			"width":    i, "height": i,
			"mode":   map[string]any{"type": "string", "enum": []any{"fit", "cover", "exact"}},
			"filter": map[string]any{"type": "string", "enum": []any{"nearest", "bilinear", "catmullrom"}},
			"x":      i, "y": i, "cw": i, "ch": i,
			"rotate":        map[string]any{"type": "integer", "enum": []any{0, 90, 180, 270}},
			"auto_orient":   b,
			"max_dim":       i, // 长边上限(等比缩放)
			"target_kb":     i, // compress:目标体积(KB)
			"long_edge":     i, // thumbnail:长边(缺省 320)
			"overwrite":     b,
			"keep_metadata": b,
		},
	}
}

// Definition 工具定义。**K1–K4 写进描述**(模型据此先摆正、注意上限、知道 GPS 被剥离)。
func (t *ImageTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "image_ops",
		Description: imageOpsDesc,
		InputSchema: imageOpsSchema(),
		// 路径参数显式声明(声明优先):源只读、目标写 —— 输出路径漏登记 = 写目标裁决形同虚设。
		PathParams: []sdk.PathParam{
			{Arg: "src", Access: sdk.PathRead, Optional: true},
			{Arg: "srcs", Access: sdk.PathRead, Many: true, Optional: true},
			{Arg: "dst", Access: sdk.PathWrite, Optional: true},
		},
	}
}

// imageOpsDesc 工具描述。**上限数字从 sdk 常量派生**(不是写死的"3000 万/8000"):
// 常量与描述各写一份,迟早对不上 —— 而这里说错会直接导致模型按错误前提操作
// (比如以为 4000 万像素也能处理,于是反复重试)。
var imageOpsDesc = "图像处理七动作(单工具多动作):" +
	"info(格式/尺寸/EXIF 概要,不解码像素) · convert(转格式) · resize(缩放) · " +
	"crop(裁剪) · rotate(90/180/270) · compress(压到目标体积) · thumbnail(缩略图)。" +
	"单张用 src,批量用 srcs(逐条报结果);dst 缺省写到 src 同目录。\n" +
	"【必须知道的四条限制】\n" +
	"1. 方向:会按 EXIF Orientation 自动摆正并烘焙,**裁剪坐标按摆正后的图算**;竖拍照片请按摆正后的尺寸给坐标。\n" +
	fmt.Sprintf("2. 上限:单张 ≤ %d 万像素且长边 ≤ %dpx(超限会报错并让你先缩小);"+
		"宽色域(Display P3)转 sRGB 会偏色,纯 Go 无 ICC 转换。\n",
		sdk.DefaultMaxPixels/10000, sdk.DefaultMaxDim) +
	"3. 动画与多图:动画 GIF/APNG 只处理首帧,多页 TIFF 只处理第一页,HEIC 多图只取主图。\n" +
	"4. 隐私:**输出默认剥离全部元数据(含 GPS 定位)**,keep_metadata 只影响信息回报、不保留 GPS。\n" +
	"默认不覆盖:dst 已存在会报错,确实要覆盖才传 overwrite: true;" +
	"**dst 必须与 src 不同**(原地覆盖会失去源文件,会被拒);覆盖是全有或全无(失败时原文件照旧)。" +
	"compress 会在目标体积内取**最高可用质量**(目标只是上限,不是越小越好)。"

// opsArgs 入参。
type opsArgs struct {
	Action string   `json:"action"`
	Src    string   `json:"src"`
	Srcs   []string `json:"srcs"`
	Dst    string   `json:"dst"`
	Format string   `json:"format"`

	Quality  int    `json:"quality"`
	Lossless bool   `json:"lossless"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Mode     string `json:"mode"`
	Filter   string `json:"filter"`

	X, Y, CW, CH int   `json:",omitempty"`
	Rotate       int   `json:"rotate"`
	AutoOrient   *bool `json:"auto_orient"`
	MaxDim       int   `json:"max_dim"`
	TargetKB     int   `json:"target_kb"`
	LongEdge     int   `json:"long_edge"`
	Overwrite    bool  `json:"overwrite"`
	KeepMetadata bool  `json:"keep_metadata"`
}

// Execute 统一入口。
func (t *ImageTool) Execute(ctx context.Context, raw string) (any, error) {
	var a opsArgs
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return errOut("image_ops: args: %v", err), nil
	}
	srcs := a.Srcs
	if a.Src != "" {
		srcs = append([]string{a.Src}, srcs...)
	}
	if len(srcs) == 0 {
		return errOut("image_ops: 需要 src(或 srcs 批量)"), nil
	}
	if a.Action == "info" {
		return t.info(ctx, srcs)
	}
	if len(srcs) > 1 && a.Dst != "" {
		return errOut("批量(dst 只能给一个)不支持:要指定落点请对每张单独调用,或让 dst 缺省(各自写回同目录)"), nil
	}
	results := make([]any, 0, len(srcs))
	failed := 0
	for _, s := range srcs {
		out, _ := t.one(ctx, a, s)
		// 逐条失败以结果里的 error 字段表达(工具层错误不是内部异常);
		// 批量里一条坏文件不该拖垮其余,所以这里数出来如实报 failed。
		if _, bad := out["error"]; bad {
			failed++
		}
		results = append(results, out)
	}
	res := map[string]any{"action": a.Action, "count": len(results), "results": results}
	if failed > 0 {
		res["failed"] = failed
		res["note"] = "部分图片失败:逐条 error 说明了原因,成功的已生效"
	}
	return res, nil
}

func errOut(format string, args ...any) map[string]any {
	return map[string]any{"error": fmt.Sprintf(format, args...)}
}

// info 只读概要(不解码像素 ⇒ 48MP 也秒回)。src 仍经沙箱裁决(相对路径按工作根解析)。
func (t *ImageTool) info(ctx context.Context, srcs []string) (any, error) {
	out := make([]any, 0, len(srcs))
	failed := 0
	for _, s := range srcs {
		p, err := t.resolve(ctx, s, false)
		if err != nil {
			failed++
			out = append(out, map[string]any{"src": s, "error": err.Error()})
			continue
		}
		info, err := t.codec.Info(p)
		if err != nil {
			failed++
			out = append(out, map[string]any{"src": p, "error": err.Error()})
			continue
		}
		out = append(out, map[string]any{
			"src": info.Path, "format": info.Format, "width": info.Width, "height": info.Height,
			"bytes": info.Bytes, "animated": info.Animated, "multi_page": info.MultiPage,
			"orientation": info.EXIF.Orientation, "has_gps": info.EXIF.HasGPS,
			"camera": strings.TrimSpace(info.EXIF.CameraMake + " " + info.EXIF.CameraModel),
		})
	}
	res := map[string]any{"action": "info", "count": len(out), "results": out,
		"codec": t.codecName(), "caps": capsOf(t.codec)}
	if failed > 0 {
		res["failed"] = failed
	}
	return res, nil
}

// one 处理一张图。
func (t *ImageTool) one(ctx context.Context, a opsArgs, srcArg string) (map[string]any, error) {
	src, err := t.resolve(ctx, srcArg, false)
	if err != nil {
		return map[string]any{"src": srcArg, "error": err.Error()}, nil
	}
	info, err := t.codec.Info(src)
	if err != nil {
		return map[string]any{"src": src, "error": err.Error()}, nil
	}
	dst, err := t.destPath(ctx, a, src, info.Format)
	if err != nil {
		return map[string]any{"src": src, "error": err.Error()}, nil
	}
	// 原地覆盖(src == dst):**先拒**。上一版会走到"覆盖前删旧目标",于是把源文件删掉、
	// 随后转换因"文件不存在"失败 —— 用户丢图,还拿到一句指向自己的报错。
	// 要原地转格式请用不同的 dst(或在外面先备份);这里不做隐式原地改写。
	if sameFile(src, dst) {
		return map[string]any{"src": src, "error": fmt.Sprintf(
			"dst 与 src 是同一个文件: %s(原地覆盖会先失去源文件;请给一个不同的 dst)", dst)}, nil
	}
	var backup string
	if fi, err := os.Lstat(dst); err == nil {
		if fi.IsDir() {
			// dst 给了个目录名(模型很容易顺手写成 `/sdcard/Pictures`):显式说清,
			// 否则落盘阶段会报一句"rename: file exists"之类与事实无关的话。
			return map[string]any{"src": src, "error": fmt.Sprintf(
				"dst 是一个目录: %s(请给到具体文件名)", dst)}, nil
		}
		if !a.Overwrite {
			return map[string]any{"src": src, "error": fmt.Sprintf(
				"目标已存在: %s(确实要覆盖就传 overwrite: true,或换一个 dst)", dst)}, nil
		}
		// 覆盖:把旧目标**挪开**(不是删掉)。成功后再删备份,失败则挪回来 ——
		// 与 tool-files 的 checkDst/commitDst 同一纪律:失败的操作不该赔掉用户原本就在的文件。
		// 顺带解决 Windows:os.Rename 覆盖不了已存在的文件,腾位之后才能落盘。
		if backup, err = stageOverwrite(dst); err != nil {
			return map[string]any{"src": src, "error": err.Error()}, nil
		}
	}
	spec, err := t.spec(a, info)
	if err != nil {
		restoreOverwrite(backup, dst, src)
		return map[string]any{"src": src, "error": err.Error()}, nil
	}
	srcBytes := info.Bytes
	if err := t.codec.Transform(src, dst, spec); err != nil {
		restoreOverwrite(backup, dst, src)
		return map[string]any{"src": src, "error": fmt.Sprintf("处理 %s: %v", src, err)}, nil
	}
	commitOverwrite(backup)
	var outBytes int64
	if fi, err := os.Stat(dst); err == nil {
		outBytes = fi.Size()
	}
	res := map[string]any{
		"src": src, "dst": dst, "src_bytes": srcBytes, "dst_bytes": outBytes,
		"src_format": info.Format, "dst_format": spec.Format,
		"stripped_metadata": []string{"exif", "gps", "icc"}, // 纯 Go 输出不含元数据
	}
	if b := imageBounds(t, dst); b != "" {
		res["output_size"] = b
	}
	if n := t.notes(info, src, spec); len(n) > 0 {
		res["notes"] = n
	}
	return res, nil
}

// notes 告知性事实(方向已烘焙 / 动画取首帧 / 宽色域偏色 / 目标体积达成)。
//
// 这些是模型下一次决策需要的"发生了什么" —— 不告诉它,下一次就会按错误前提继续。
// 参数传的是**已经取到的概要**而不是路径:上一版每个事实各读一遍文件
// (一次转换要开五次同一个文件),批量 20 张时是纯浪费。
func (t *ImageTool) notes(info sdk.ImageInfo, src string, spec sdk.TransformSpec) []string {
	var out []string
	if info.EXIF.Orientation > 1 {
		out = append(out, fmt.Sprintf("已按 EXIF Orientation=%d 摆正并烘焙", info.EXIF.Orientation))
	}
	if info.Animated {
		out = append(out, "动画/多帧:只处理首帧")
	}
	if info.MultiPage {
		out = append(out, "多页 TIFF:只处理第一页")
	}
	if spec.StripAll {
		out = append(out, "已剥离 EXIF/GPS 等元数据(输出不含定位信息)")
	}
	// 宽色域只在**真读得出 ICC 段**时提示(读一次 EXIF 头即可,不做像素转换)
	if readEXIF(src).P3 {
		out = append(out, "源带 ICC 宽色域(P3):纯 Go 无 ICC 转换,输出会偏色")
	}
	if spec.TargetBytes > 0 {
		out = append(out, fmt.Sprintf("按目标体积 %d KB 迭代质量达成", spec.TargetBytes/1024))
	}
	return out
}

// sameFile 两个路径是否指向同一个文件(先比清理后的字符串;两边都存在时再用
// os.SameFile 兜住大小写不敏感/符号链接的等价形态 —— macOS 与 Windows 上
// "同一条路径的两种写法"是常态)。
func sameFile(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

// stageOverwrite 覆盖前把旧目标挪到同目录的临时名(返回备份路径)。
// 同目录 = 同一文件系统 ⇒ rename 是原子的、几乎不花时间。
func stageOverwrite(dst string) (string, error) {
	backup := fmt.Sprintf("%s.gah-bak-%d", dst, time.Now().UnixNano())
	if err := os.Rename(dst, backup); err != nil {
		return "", fmt.Errorf("覆盖前腾位失败(旧目标 %s 挪不动): %w", dst, err)
	}
	return backup, nil
}

// commitOverwrite 转换成功:丢掉备份(失败只记日志 —— 结果已经写好了)。
func commitOverwrite(backup string) {
	if backup == "" {
		return
	}
	if err := os.Remove(backup); err != nil {
		fmt.Fprintf(os.Stderr, "image_ops: 覆盖后清理备份 %s 失败(可用它找回旧版本): %v\n", backup, err)
	}
}

// restoreOverwrite 转换失败:把旧目标挪回来,保证"失败 = 什么都没变"。
func restoreOverwrite(backup, dst, src string) {
	if backup == "" {
		return
	}
	if err := os.Rename(backup, dst); err != nil {
		fmt.Fprintf(os.Stderr, "image_ops: 转换失败且旧目标恢复不了(旧版本在 %s,源在 %s): %v\n",
			backup, src, err)
	}
}

// spec 由动作与参数装配变换规格(各动作只填自己那几个字段)。
func (t *ImageTool) spec(a opsArgs, info sdk.ImageInfo) (sdk.TransformSpec, error) {
	spec := sdk.TransformSpec{
		Format:      sdk.ImageFormat(strings.ToLower(a.Format)),
		Quality:     a.Quality,
		Lossless:    a.Lossless,
		AutoOrient:  a.AutoOrient == nil || *a.AutoOrient, // 缺省开(K1)
		StripAll:    true,                                 // 缺省剥离(K4)
		Rotate:      a.Rotate,
		TargetBytes: a.TargetKB * 1024,
	}
	switch a.Action {
	case "convert":
		if spec.Format == "" {
			return spec, fmt.Errorf("convert 需要 format(jpeg/png/gif/bmp/tiff/webp)")
		}
	case "resize":
		if a.Width <= 0 && a.Height <= 0 && a.MaxDim <= 0 {
			return spec, fmt.Errorf("resize 需要 width/height(或 max_dim 指定长边)")
		}
		mode := orDefault(a.Mode, "fit")
		if a.Width > 0 || a.Height > 0 {
			// **只给一边也算数**:按比例推出另一边(fit)。
			// 上一版只接受"宽高都给",于是 `resize width=800` 这种最常见的写法会落进
			// `else` 分支、MaxDim 为 0 ⇒ **什么都不做却报成功**(调用方拿到原尺寸还以为成了)。
			// exact 需要两边都给(否则"强制尺寸"没有第二边可依)。
			if mode == "exact" && (a.Width <= 0 || a.Height <= 0) {
				return spec, fmt.Errorf("mode=exact 需要 width 与 height 都给出(单边用 fit)")
			}
			spec.Resize = &sdk.ResizeSpec{W: a.Width, H: a.Height, Mode: mode, Filter: a.Filter}
			if a.MaxDim > 0 {
				return spec, fmt.Errorf("width/height 与 max_dim 互斥:一次只给一种尺寸口径")
			}
			break
		}
		spec.MaxDim = a.MaxDim
	case "crop":
		if a.CW <= 0 || a.CH <= 0 {
			return spec, fmt.Errorf("crop 需要 cw/ch(裁剪宽高),x/y 缺省为左上角(0,0)")
		}
		spec.Crop = &sdk.CropSpec{X: a.X, Y: a.Y, W: a.CW, H: a.CH}
	case "rotate":
		if a.Rotate != 90 && a.Rotate != 180 && a.Rotate != 270 {
			return spec, fmt.Errorf("rotate 需要 90/180/270 之一(按 EXIF 自动摆正已默认开启,不需要额外指定)")
		}
	case "compress":
		if a.TargetKB <= 0 {
			return spec, fmt.Errorf("compress 需要 target_kb(目标体积 KB)")
		}
		if spec.Format == "" {
			spec.Format = sdk.FormatJPEG
		}
	case "thumbnail":
		edge := a.LongEdge
		if edge <= 0 {
			edge = 320
		}
		spec.MaxDim = edge
	default:
		return spec, fmt.Errorf("未知 action %q(可选 info/convert/resize/crop/rotate/compress/thumbnail)", a.Action)
	}
	// 动作没给 format 时:缺省保持源格式(convert/compress 已在上面积压)。
	if spec.Format == "" {
		spec.Format = info.Format
	}
	return spec, nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// destPath 目标路径:显式 dst > <src 同目录>/<基名>_<action>.<目标格式扩展名>。
//
// **两条路径都过 resolve(…, write=true)** —— 默认 dst 也是写目标,漏裁决就是绕过沙箱
// 的写检查(工具自己派生的路径不等于"不是用户给的写目标")。
func (t *ImageTool) destPath(ctx context.Context, a opsArgs, src string, _ sdk.ImageFormat) (string, error) {
	if strings.TrimSpace(a.Dst) != "" {
		return t.resolve(ctx, a.Dst, true)
	}
	ext := extFor(sdk.ImageFormat(strings.ToLower(a.Format)))
	if ext == "" {
		ext = filepath.Ext(src)
		if ext == "" {
			ext = ".png"
		}
	}
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	return t.resolve(ctx, filepath.Join(filepath.Dir(src), base+"_"+a.Action+ext), true)
}

func extFor(f sdk.ImageFormat) string {
	switch f {
	case sdk.FormatJPEG:
		return ".jpg"
	case sdk.FormatPNG:
		return ".png"
	case sdk.FormatGIF:
		return ".gif"
	case sdk.FormatBMP:
		return ".bmp"
	case sdk.FormatTIFF:
		return ".tiff"
	case sdk.FormatWEBP, sdk.FormatWEBPLossy, sdk.FormatWEBPNoLoss:
		return ".webp"
	}
	return ""
}

// resolve 路径裁决(与 tool-files 同一实现:sdk.ResolveSandboxPath)。
func (t *ImageTool) resolve(ctx context.Context, path string, write bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("缺少路径参数")
	}
	if sdk.LooksLikeURLPath(path) {
		return "", fmt.Errorf("path 是 URL 而不是本地文件路径: %s", path)
	}
	return sdk.ResolveSandboxPath(ctx, t.sb, path, write)
}

func (t *ImageTool) codecName() string {
	if _, ok := t.codec.(*GoCodec); ok {
		return "builtin-go"
	}
	return "host"
}

func capsOf(c sdk.ImageCodec) map[string]any {
	caps := c.Caps()
	return map[string]any{
		"decode": caps.Decode, "encode": caps.Encode,
		"max_pixels": caps.MaxPixels, "can_downscale": caps.CanDownscale,
	}
}

// imageBounds 输出尺寸(读配置头即可;读不到就不报,不该因此让整次调用失败)。
func imageBounds(t *ImageTool, path string) string {
	info, err := t.codec.Info(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%dx%d", info.Width, info.Height)
}
