// 本文件:内置**纯 Go** ImageCodec 实现(F4 的桌面能力;Android 侧由壳侧原生实现补齐
// HEIC/AVIF 解码与有损 WebP 编码,本文件在那边作为被装饰器内嵌的基座)。
//
// 四个必做的坑(方案 §M2/K1–K4)在这里怎么落地:
//
//	K1 EXIF 方向:Go 的 image/jpeg 不处理(golang/go#4341)⇒ 先读 Orientation 把像素摆正,
//	   裁剪坐标按摆正后算,输出**烘焙**且不带 Orientation(不写 EXIF = 一并清除)。
//	K2 大图内存:48MP ≈ 200 MB RGBA,Go 不能边解边降采样 ⇒ 硬上限 30MP / 长边 8000,
//	   超限**显式报错**并给出路(缩小后重试 / 宿主原生实现可降采样)。
//	K3 动画/多图:动画 GIF/APNG 取首帧并在结果里说明;多页 TIFF 取第一页。
//	K4 色彩与隐私:纯 Go 无 ICC 转换(Display P3 会偏色,写进工具描述与 README);
//	   输出**默认不写任何元数据** ⇒ GPS 等隐私字段默认剥离(要保留须显式 keep_metadata)。
package toolimage

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
	"golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/tiff"
	_ "golang.org/x/image/webp" // WebP 仅解码(编码交给宿主或内置不支持)
)

// drawOp 缩放用 Src 覆盖(不留透明底色差异)。
const drawOp = draw.Src

// GoCodec 内置纯 Go 实现(进程内单例)。
type GoCodec struct{}

// NewGoCodec 构造内置实现。
func NewGoCodec() *GoCodec { return &GoCodec{} }

// Caps 自述支持面(桌面 = 无宿主注册时的能力):WebP **仅解码**,无 HEIC/AVIF,不能降采样。
func (c *GoCodec) Caps() sdk.CodecCaps {
	return sdk.CodecCaps{
		Decode: []sdk.ImageFormat{sdk.FormatJPEG, sdk.FormatPNG, sdk.FormatGIF,
			sdk.FormatBMP, sdk.FormatTIFF, sdk.FormatWEBP},
		Encode:       []sdk.ImageFormat{sdk.FormatJPEG, sdk.FormatPNG, sdk.FormatGIF, sdk.FormatBMP, sdk.FormatTIFF},
		MaxPixels:    sdk.DefaultMaxPixels,
		CanDownscale: false,
	}
}

// SupportsDecode/SupportsEncode 选路与报错文案共用。
func (c *GoCodec) SupportsDecode(f sdk.ImageFormat) bool { return hasFormat(c.Caps().Decode, f) }
func (c *GoCodec) SupportsEncode(f sdk.ImageFormat) bool { return hasFormat(c.Caps().Encode, f) }

func hasFormat(list []sdk.ImageFormat, f sdk.ImageFormat) bool {
	for _, x := range list {
		if x == f {
			return true
		}
	}
	return false
}

// hasAlpha 图像是否带透明通道。
//
// **不能靠 color.Model 判**:PNG 解码器给的是 `*color.modelFunc`(不可比、不可读),
// 于是"等值比较"对最常见的输入直接失效 —— 而上一版更糟,写的是 `cfg.ColorModel != nil`,
// **恒为真**,一个永远为真的字段等于没有字段(模型据此判断"转 png 会不会多底色"必然被误导)。
// 所以按**容器头**的真凭据判:
//
//	PNG: IHDR 的色彩类型 4(灰度+α)/6(RGBA);另外 tRNS 块给 0/2/3 型也加了透明
//	GIF: 图形控制扩展的透明色标志位
//	其余(BMP/TIFF/JPEG/WebP):保守 false —— 它们的 alpha 要么罕见,要么要读更深的结构,
//	     而"错报有 alpha"会让模型白转一次 png(无损失),"错报没有"最多是少一句提示
func hasAlpha(cm color.Model, path string) bool {
	if detectAlphaInContainer(path) {
		return true
	}
	// 兜底:宿主实现/其它解码器可能给得出可比的色彩模型
	switch cm {
	case color.NRGBA64Model, color.AlphaModel, color.Alpha16Model:
		return true
	}
	return false
}

// detectAlphaInContainer 按文件头判透明通道(读几百字节,不解码像素)。
func detectAlphaInContainer(path string) bool {
	fh, err := os.Open(path)
	if err != nil {
		return false
	}
	defer fh.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(fh, head)
	head = head[:n]
	switch {
	case len(head) >= 26 && bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		switch head[25] { // 签名8 + 长度4 + "IHDR"4 + 宽4 + 高4 + 位深1 → 色彩类型在 25
		case 4, 6: // 灰度+α / RGBA
			return true
		}
		return bytes.Contains(head, []byte("tRNS")) // 调色板/真彩的透明色块
	case len(head) >= 6 && (string(head[:6]) == "GIF87a" || string(head[:6]) == "GIF89a"):
		return gifHasTransparentIndex(head)
	}
	return false
}

// gifHasTransparentIndex 图形控制扩展的 packed 字节 bit0 = 有透明色。
func gifHasTransparentIndex(b []byte) bool {
	for i := 0; i+3 < len(b); i++ {
		// 0x21 扩展引导 + 0xF9 图形控制标签 + 块长(4)+ packed
		if b[i] == 0x21 && b[i+1] == 0xF9 && b[i+2] == 0x04 {
			return b[i+3]&0x01 != 0
		}
	}
	return false
}

// FormatByExt 按扩展名推断目标格式(工具用它给 dst 缺后缀的情况兜底)。
func FormatByExt(path string) sdk.ImageFormat {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".jpe":
		return sdk.FormatJPEG
	case ".png":
		return sdk.FormatPNG
	case ".gif":
		return sdk.FormatGIF
	case ".bmp":
		return sdk.FormatBMP
	case ".tif", ".tiff":
		return sdk.FormatTIFF
	case ".webp":
		return sdk.FormatWEBP
	case ".heic", ".heif":
		return sdk.FormatHEIC
	case ".avif":
		return sdk.FormatAVIF
	}
	return ""
}

// sniffFormat 读文件头判断真实格式(不信扩展名:模型经常把 jpg 存成 .png)。
func sniffFormat(path string) (sdk.ImageFormat, error) {
	fh, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("文件不存在: %s", path)
		}
		return "", err
	}
	defer fh.Close()
	var head [512]byte
	n, _ := fh.Read(head[:])
	_, format, err := image.DecodeConfig(bytes.NewReader(head[:n]))
	if err != nil {
		// 兜底:EXIF 里的 JPEG 头可能超出 512B;再用 magic 判一次。
		switch {
		case bytes.HasPrefix(head[:n], []byte("\xff\xd8\xff")):
			return sdk.FormatJPEG, nil
		case bytes.HasPrefix(head[:n], []byte("\x89PNG\r\n\x1a\n")):
			return sdk.FormatPNG, nil
		case bytes.HasPrefix(head[:n], []byte("GIF8")):
			return sdk.FormatGIF, nil
		case bytes.HasPrefix(head[:n], []byte("II*\x00")), bytes.HasPrefix(head[:n], []byte("MM\x00*")):
			return sdk.FormatTIFF, nil
		case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP":
			return sdk.FormatWEBP, nil
		case len(head) >= 12 && string(head[4:8]) == "ftyp":
			switch string(head[8:12]) {
			case "heic", "heix", "hevc", "heim", "heis", "mif1", "msf1":
				return sdk.FormatHEIC, nil
			case "avif", "avis":
				return sdk.FormatAVIF, nil
			}
		}
		return "", fmt.Errorf("认不出图像格式(不是 jpeg/png/gif/bmp/tiff/webp;HEIC/AVIF 需要宿主原生实现)")
	}
	return FormatByExt("_." + format), nil
}

// Info 不含像素的概要(不解码整幅 ⇒ 48MP 也能秒回)。
func (c *GoCodec) Info(path string) (sdk.ImageInfo, error) {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sdk.ImageInfo{}, fmt.Errorf("文件不存在: %s", path)
		}
		return sdk.ImageInfo{}, err
	}
	if st.IsDir() {
		return sdk.ImageInfo{}, fmt.Errorf("这是目录不是图像: %s", path)
	}
	if !st.Mode().IsRegular() {
		// fifo/socket/设备上的 os.Open **会永久阻塞**(实测:fifo 无写端时整回合卡死),
		// 而 os.Stat 已经跟随了符号链接 ⇒ 这里拒的就是真·特殊文件。
		return sdk.ImageInfo{}, fmt.Errorf("不是普通文件(设备/fifo/socket 之类): %s"+
			"(读取它可能永久阻塞,已拒绝)", path)
	}
	format, err := sniffFormat(path)
	if err != nil {
		return sdk.ImageInfo{}, err
	}
	exif := readEXIF(path)
	fh, err := os.Open(path)
	if err != nil {
		return sdk.ImageInfo{}, err
	}
	defer fh.Close()
	cfg, _, decErr := image.DecodeConfig(bufio.NewReader(fh))
	if decErr != nil {
		if !c.SupportsDecode(format) {
			return sdk.ImageInfo{}, fmt.Errorf("%s 需要宿主原生实现(桌面端不支持该格式的解码)", format)
		}
		return sdk.ImageInfo{}, fmt.Errorf("读图像头失败 %s: %v", path, decErr)
	}
	return sdk.ImageInfo{
		Path: path, Format: format, Width: cfg.Width, Height: cfg.Height, Bytes: st.Size(),
		Animated: isAnimatedGIF(path), MultiPage: format == sdk.FormatTIFF,
		Alpha: hasAlpha(cfg.ColorModel, path), EXIF: exif.summary(),
	}, nil
}

// Transform 执行一次变换并写盘(src → dst;dst 由调用方裁决过)。
//
// 写盘一律「同目录临时文件 + rename」:中途失败不会在目标位置留半截图(D2)。
func (c *GoCodec) Transform(src, dst string, spec sdk.TransformSpec) error {
	info, err := c.Info(src)
	if err != nil {
		return err
	}
	// 目标格式口径(D2):显式 format > dst 扩展名 > **保持源格式**。
	// 靠扩展名是为了 `convert` 不给 format 时不至于把 jpg 悄悄存成 png。
	if spec.Format == "" {
		spec.Format = FormatByExt(dst)
	}
	if spec.Format == "" {
		spec.Format = info.Format
	}
	img, err := c.decodeOriented(src)
	if err != nil {
		return err
	}
	if img, err = c.applyOps(img, spec); err != nil {
		return err
	}
	// 纯 Go 输出从不写元数据 ⇒ GPS/EXIF 一律剥离(K4);告知性事实由工具层汇总给模型。
	return writeAtomic(dst, func(w io.Writer) error { return c.encode(w, img, spec) })
}

// decodeOriented 解码 + 摆正(K1),并施加 K2 的硬上限。
func (c *GoCodec) decodeOriented(path string) (image.Image, error) {
	format, err := sniffFormat(path)
	if err != nil {
		return nil, err
	}
	if !c.SupportsDecode(format) {
		return nil, fmt.Errorf("%s 不在内置纯 Go 的解码面(%v);需要宿主原生实现(Android 壳侧才有)",
			format, c.Caps().Decode)
	}
	// K2:先用配置(不解码像素)判上限 —— 否则 48MP 的文件先把内存吃光才报错。
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	cfg, _, cerr := image.DecodeConfig(fh)
	fh.Close()
	if cerr != nil {
		return nil, fmt.Errorf("读图像头失败 %s: %v", path, cerr)
	}
	if cfg.Width > sdk.DefaultMaxDim || cfg.Height > sdk.DefaultMaxDim ||
		int64(cfg.Width)*int64(cfg.Height) > sdk.DefaultMaxPixels {
		return nil, tooBigErr(cfg.Width, cfg.Height)
	}
	fh2, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh2.Close()
	src, _, derr := image.Decode(bufio.NewReader(fh2))
	if derr != nil {
		return nil, fmt.Errorf("解码 %s 失败: %v", path, derr)
	}
	out, _ := applyOrientation(src, readEXIF(path).Orientation) // K1:摆正并烘焙
	return out, nil
}

func tooBigErr(w, h int) error {
	return fmt.Errorf("图像过大 %dx%d(上限 %d 万像素 / 长边 %d px;纯 Go 不能边解边降采样,内存会先炸):"+
		"先用 image_ops resize/thumbnail 缩小再处理,或转成小图", w, h, sdk.DefaultMaxPixels/10000, sdk.DefaultMaxDim)
}

// applyOps 按规格做裁剪/缩放/旋转,并定出目标格式与编码参数。
func (c *GoCodec) applyOps(img image.Image, spec sdk.TransformSpec) (image.Image, error) {
	if spec.Crop != nil && (spec.Resize != nil || spec.MaxDim > 0) {
		return img, fmt.Errorf("crop 与 resize/max_dim 互斥:一次只做一种尺寸变更(先 crop,再另一次 resize)")
	}
	if spec.Crop != nil {
		cr := *spec.Crop
		if cr.W <= 0 || cr.H <= 0 {
			return img, fmt.Errorf("裁剪区域宽高必须为正: %dx%d", cr.W, cr.H)
		}
		b := img.Bounds()
		if cr.X < b.Min.X || cr.Y < b.Min.Y || cr.X+cr.W > b.Max.X || cr.Y+cr.H > b.Max.Y {
			return img, fmt.Errorf("裁剪区域 %d,%d %dx%d 超出图像范围(摆正后尺寸 %dx%d)",
				cr.X, cr.Y, cr.W, cr.H, b.Dx(), b.Dy())
		}
		img = cropRegion(img, image.Rect(cr.X, cr.Y, cr.X+cr.W, cr.Y+cr.H))
	}
	if spec.Resize != nil {
		img = resizeImage(img, spec.Resize)
	}
	if spec.MaxDim > 0 {
		img = fitMaxDim(img, spec.MaxDim, "catmullrom")
	}
	switch spec.Rotate {
	case 0:
	case 90, 180, 270:
		img = rotateImage(img, spec.Rotate)
	default:
		return img, fmt.Errorf("rotate 只支持 0/90/180/270,得 %d", spec.Rotate)
	}
	return img, nil
}

// encode 按目标格式落盘(TargetBytes > 0 时迭代质量)。
func (c *GoCodec) encode(w io.Writer, img image.Image, spec sdk.TransformSpec) error {
	format := spec.Format
	if !c.SupportsEncode(format) {
		return fmt.Errorf("内置纯 Go 不支持编码 %s(支持 %v);有损/无损 WebP 与 HEIC/AVIF 由宿主原生实现提供",
			format, c.Caps().Encode)
	}
	if spec.TargetBytes > 0 {
		if format != sdk.FormatJPEG && format != sdk.FormatWEBP && format != sdk.FormatWEBPLossy {
			return fmt.Errorf("目标体积只对 jpeg/webp 有意义,得 %s(无损格式改用 resize 缩尺寸)", format)
		}
		return encodeToTarget(w, img, format, spec)
	}
	return encodeOnce(w, img, format, spec)
}

// encodeOnce 单次编码。
func encodeOnce(w io.Writer, img image.Image, format sdk.ImageFormat, spec sdk.TransformSpec) error {
	switch format {
	case sdk.FormatJPEG:
		// 不写任何 EXIF ⇒ K1 的方向已烘焙、K4 的 GPS 被剥离。
		return jpeg.Encode(w, img, &jpeg.Options{Quality: qualityOr(spec, 85)})
	case sdk.FormatPNG:
		enc := png.Encoder{CompressionLevel: png.DefaultCompression}
		if spec.Lossless {
			enc.CompressionLevel = png.BestCompression
		}
		return enc.Encode(w, img)
	case sdk.FormatGIF:
		return gif.Encode(w, img, &gif.Options{NumColors: 256})
	case sdk.FormatBMP:
		return bmp.Encode(w, img)
	case sdk.FormatTIFF:
		return tiff.Encode(w, img, &tiff.Options{Compression: tiff.Deflate})
	}
	return fmt.Errorf("不支持的编码格式: %s", format)
}

func qualityOr(spec sdk.TransformSpec, def int) int {
	if spec.Quality <= 0 {
		return def
	}
	if spec.Quality > 100 {
		return 100
	}
	return spec.Quality
}

// encodeToTarget 把体积压到目标以内,**同时尽量保住画质**:找"预算内可用的最高质量"。
//
// 上一版的搜索方向是反的(一旦落进预算就继续往下压质量),实测目标 200 KB 时输出只有
// 41 KB —— 占预算 20%,也就是白白丢掉了八成可用的画质,而调用方从结果里**看不出**自己
// 损失了什么(只知道"达成了目标")。压缩的目标从来是"不超过 X 且尽量清晰",不是"越小越好"。
func encodeToTarget(w io.Writer, img image.Image, format sdk.ImageFormat, spec sdk.TransformSpec) error {
	target := int64(spec.TargetBytes)
	// 先取质量下限的产物:既用于"根本压不到"的如实报错(数字要真,不能是最后一次采样),
	// 也作为二分兜底(它一定 ≤ 预算才会往下走)。
	smallest, err := encodeAt(img, format, spec, minEncodeQuality)
	if err != nil {
		return err
	}
	if int64(len(smallest)) > target {
		return fmt.Errorf("压到质量下限(%d)仍超出目标体积:目标 %d 字节,最小可达 %d 字节"+
			"(想要更小只能缩尺寸:先用 resize/thumbnail)", minEncodeQuality, target, len(smallest))
	}
	best := smallest
	lo, hi := minEncodeQuality, maxEncodeQuality
	for lo <= hi {
		mid := (lo + hi) / 2
		buf, err := encodeAt(img, format, spec, mid)
		if err != nil {
			return err
		}
		if int64(len(buf)) <= target {
			best = buf // 这个质量可行 → 记下来,再往上试
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	_, err = w.Write(best)
	return err
}

// 质量搜索区间。上限 95 而不是 100:JPEG 在 100 附近体积暴涨而肉眼收益趋零。
const (
	minEncodeQuality = 10
	maxEncodeQuality = 95
)

// encodeAt 以指定质量编码一份(内存里的完整产物,供体积比较)。
func encodeAt(img image.Image, format sdk.ImageFormat, spec sdk.TransformSpec, quality int) ([]byte, error) {
	var buf bytes.Buffer
	s := spec
	s.Quality = quality
	s.TargetBytes = 0 // 防止递归(此时只比较体积)
	if err := encodeOnce(&buf, img, format, s); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ── 几何变换 ────────────────────────────────────────────────────

// resizeImage 按 ResizeSpec 缩放。
//
// cover(铺满后裁)走**一次采样**:直接从源图上取居中的子矩形缩到目标尺寸。
// 上一版是"先按较大缩放比缩到覆盖,再把整幅缩一次"—— 两次重采样等于把细节糊一遍
// (缩放核的负空间与锐化在第二次都被放大),而两次的差别用户只会觉得"图有点糊"。
func resizeImage(img image.Image, r *sdk.ResizeSpec) image.Image {
	mode := r.Mode
	if mode == "" {
		mode = "fit"
	}
	filter := pickFilter(r.Filter)
	sb := img.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 || sh <= 0 || (r.W <= 0 && r.H <= 0) {
		return img
	}
	// 单边目标(只给宽或只给高):按源比例推出另一边。fit/cover 的语义里"一边定死"
	// 本来就是常见用法(`resize width=800`),不处理它等于静默忽略这个参数。
	if r.W > 0 && r.H <= 0 {
		r.H = max(1, int(float64(sh)*float64(r.W)/float64(sw)+0.5))
	}
	if r.H > 0 && r.W <= 0 {
		r.W = max(1, int(float64(sw)*float64(r.H)/float64(sh)+0.5))
	}
	if mode == "exact" {
		dst := image.NewRGBA(image.Rect(0, 0, r.W, r.H))
		filter.Scale(dst, dst.Bounds(), img, sb, drawOp, nil)
		return dst
	}
	if mode == "cover" {
		// 源上居中的子矩形,宽高比与目标一致
		scale := max(float64(r.W)/float64(sw), float64(r.H)/float64(sh))
		cw := min(float64(sw), float64(r.W)/scale)
		ch := min(float64(sh), float64(r.H)/scale)
		sub := image.Rect(
			sb.Min.X+(sw-int(cw))/2, sb.Min.Y+(sh-int(ch))/2,
			sb.Min.X+(sw+int(cw))/2, sb.Min.Y+(sh+int(ch))/2,
		)
		dst := image.NewRGBA(image.Rect(0, 0, r.W, r.H))
		filter.Scale(dst, dst.Bounds(), img, sub, drawOp, nil)
		return dst
	}
	// fit **会放大**:目标比源大时按包围盒等比放大(与 ImageMagick -resize 同口径)。
	// 反面选项(只缩不放)会"静默忽略调用方给的尺寸",比放大更糟 ——
	// 需要"别放大"的语义时用 max_dim / thumbnail(它们本就按长边封顶)。
	scale := min(float64(r.W)/float64(sw), float64(r.H)/float64(sh))
	w := max(1, int(float64(sw)*scale+0.5))
	h := max(1, int(float64(sh)*scale+0.5))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	filter.Scale(dst, dst.Bounds(), img, sb, drawOp, nil)
	return dst
}

// cropRegion 取源上的一块。SubImage 是零拷贝(快),但并非所有 image 实现都提供它 ——
// 缺了就复制一块。**关键是不用无检查的类型断言**:那会把"某个解码器/宿主实现返回了自定义
// image"变成一次 panic,而工具 panic 对用户是无法解释的崩溃(宁慢不崩)。
func cropRegion(img image.Image, r image.Rectangle) image.Image {
	if si, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return si.SubImage(r)
	}
	dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), img, r.Min, drawOp)
	return dst
}

// fitMaxDim 等比缩放到长边 ≤ maxDim。
func fitMaxDim(img image.Image, maxDim int, filter string) image.Image {
	b := img.Bounds()
	long := max(b.Dx(), b.Dy())
	if long <= maxDim || maxDim <= 0 {
		return img
	}
	scale := float64(maxDim) / float64(long)
	w := max(1, int(float64(b.Dx())*scale+0.5))
	h := max(1, int(float64(b.Dy())*scale+0.5))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	pickFilter(filter).Scale(dst, dst.Bounds(), img, img.Bounds(), drawOp, nil)
	return dst
}

// rotateImage 顺时针旋转 90/180/270。
func rotateImage(img image.Image, deg int) image.Image {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	var dst *image.RGBA
	switch deg {
	case 180:
		dst = image.NewRGBA(image.Rect(0, 0, sw, sh))
	case 90, 270:
		dst = image.NewRGBA(image.Rect(0, 0, sh, sw))
	default:
		return img
	}
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			src := img.At(b.Min.X+x, b.Min.Y+y)
			var nx, ny int
			switch deg {
			case 90:
				nx, ny = sh-1-y, x
			case 180:
				nx, ny = sw-1-x, sh-1-y
			case 270:
				nx, ny = y, sw-1-x
			}
			dst.Set(nx, ny, src)
		}
	}
	return dst
}

// applyOrientation 按 EXIF Orientation 摆正(1..8),返回摆正后的图与实际应用的值。
// 完整实现 8 个朝向 —— 手机竖拍照片(Orientation=6/8)是最常见的"拍出来躺着"来源。
func applyOrientation(img image.Image, orientation int) (image.Image, int) {
	if orientation < 2 || orientation > 8 {
		return img, 1
	}
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	swap := orientation >= 5
	w, h := sw, sh
	if swap {
		w, h = sh, sw
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			c := img.At(b.Min.X+x, b.Min.Y+y)
			var nx, ny int
			switch orientation {
			case 2:
				nx, ny = sw-1-x, y
			case 3:
				nx, ny = sw-1-x, sh-1-y
			case 4:
				nx, ny = x, sh-1-y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = sh-1-y, x
			case 7:
				nx, ny = sh-1-y, sw-1-x
			case 8:
				nx, ny = y, sw-1-x
			}
			dst.Set(nx, ny, c)
		}
	}
	return dst, orientation
}

// ── 编码器小工具 ────────────────────────────────────────────────

// isAnimatedGIF 动画 GIF 取首帧(K3)。
//
// **只扫流结构数帧,不解码**:上一版用 gif.DecodeAll 判"是不是动画",那会把**所有帧**解码
// 进内存 —— 一个 20MB 的动画 GIF 在这里能吃掉几百 MB,而它只是被 Info 顺带问了一句。
// 帧数按 GIF 流的图像描述符(0x2C)计,只读前若干字节即可(动画必然在文件头附近出现第二个)。
func isAnimatedGIF(path string) bool {
	return countGIFImages(path) > 1
}

// gifFrameCap 帧数计数上限。判"是不是动画"只要 >1,而"到底几帧"对使用者没有意义,
// 故数到上限就停(仍然只扫结构,不解码任何一帧)。
const gifFrameCap = 100

// gifHeaderLen GIF 头 + 逻辑屏描述符 + **最坏情况**的全局颜色表(256 项)。
const gifHeaderLen = 6 + 7 + 768

// countGIFImages 数 GIF 里的图像描述符个数(≥2 即动画)。解析失败按 1 处理(当静态)。
//
// 按 GIF 流结构跳:0x2C 图像描述符(再 9 字节 + LZW 最小码长 + 数据子块)、
// 0x21 扩展(标签 + 子块)、0x3B 结束。只读文件头之后的一段 —— 动画第二帧必然在里面,
// 再往后就是为计数白花内存。
func countGIFImages(path string) int {
	fh, err := os.Open(path)
	if err != nil {
		return 1
	}
	defer fh.Close()
	head := make([]byte, gifHeaderLen)
	n, err := io.ReadFull(fh, head)
	if err != nil && n < 13 {
		return 1
	}
	head = head[:n]
	if string(head[:6]) != "GIF87a" && string(head[:6]) != "GIF89a" {
		return 1
	}
	// 全局颜色表:逻辑屏描述符的 packed 位 bit7 = 有表,大小 = 3 << (N+1)
	pos := 13
	if head[10]&0x80 != 0 {
		pos += 3 << ((head[10] & 0x07) + 1)
	}
	// 跳到第一个块(头部缓冲区可能已越过它,也可能还没到 —— 两种都要处理对)
	if pos > n {
		if _, err := fh.Seek(int64(pos-n), io.SeekStart); err != nil {
			return 1
		}
	} else {
		if _, err := fh.Seek(int64(pos), io.SeekStart); err != nil {
			return 1
		}
	}
	rest := make([]byte, 64<<10)
	r, _ := fh.Read(rest)
	rest = rest[:r]
	count := 0 // 数图像描述符个数(不是"1 + 个数",否则单帧 GIF 会被判成动画)
	for i := 0; i < len(rest); {
		switch rest[i] {
		case 0x2C: // 图像描述符
			count++
			if i+10 > len(rest) {
				return count
			}
			// 9 字节描述符(左/上/宽/高/包字段)+ 可选的**局部颜色表** + LZW 最小码长 + 数据子块。
			// 漏掉局部颜色表会让解析从色表中间继续 —— 那是彩色噪声,数出来的帧数是随机的
			// (本仓的动画 GIF 正是每帧带局部色表的情况)。
			i += 10
			if rest[i-1]&0x80 != 0 { // packed bit7 = 有局部颜色表
				i += 3 << ((rest[i-1] & 0x07) + 1)
			}
			i++ // LZW 最小码长
			i += skipSubBlocks(rest[i:])
		case 0x21: // 扩展:标签 + 子块
			if i+2 > len(rest) {
				return count
			}
			i += 2
			i += skipSubBlocks(rest[i:])
		case 0x3B: // trailer
			return count
		default:
			i += skipSubBlocks(rest[i:])
		}
		if count >= gifFrameCap {
			return count // 计数上限:只为了回答"是不是动画/多不多",不追求精确到帧
		}
	}
	if count == 0 {
		return 1 // 没数到描述符(截断/畸形):按静态处理,不谎报动画
	}
	return count
}

// skipSubBlocks 跳过一串 [长度][数据] 子块(以 0 长度块收尾);越界返回剩余长度。
func skipSubBlocks(b []byte) int {
	for i := 0; i < len(b); {
		if b[i] == 0 {
			return i + 1
		}
		i += int(b[i]) + 1
	}
	return len(b)
}

func pickFilter(name string) xdraw.Scaler {
	switch name {
	case "nearest":
		return xdraw.NearestNeighbor
	case "bilinear":
		return xdraw.BiLinear
	default:
		return xdraw.CatmullRom
	}
}

// writeAtomic 同目录临时文件 + fsync + rename(失败清理临时文件,不留半截)。
//
// **前置契约:调用方已保证 dst 不存在**(image_ops 侧:已存在且未 overwrite ⇒ 提前报错;
// overwrite=true ⇒ 已把旧目标腾到 `<dst>.gah-bak-<纳秒>`)。因此这里的 rename 不需要
// 覆盖已存在的文件 —— 那在 Windows 上会失败。
func writeAtomic(dst string, enc func(w io.Writer) error) (err error) {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".gah-tmp-*")
	if err != nil {
		return fmt.Errorf("在 %s 建临时文件失败: %w", dir, err)
	}
	name := tmp.Name()
	defer func() {
		tmp.Close()
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	if err = enc(tmp); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, dst); err != nil {
		return err
	}
	return nil
}

var _ = errors.Is
