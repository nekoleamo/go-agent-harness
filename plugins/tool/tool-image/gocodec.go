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
	animated := isAnimatedGIF(path)
	return sdk.ImageInfo{
		Path: path, Format: format, Width: cfg.Width, Height: cfg.Height, Bytes: st.Size(),
		Animated: animated, MultiPage: format == sdk.FormatTIFF, Alpha: cfg.ColorModel != nil,
		EXIF: exif.summary(),
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
		img = img.(interface {
			SubImage(image.Rectangle) image.Image
		}).SubImage(image.Rect(cr.X, cr.Y, cr.X+cr.W, cr.Y+cr.H))
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

// encodeToTarget 二分质量逼近目标体积(±10% 内即算达成;到质量 10 仍超限就如实报错)。
func encodeToTarget(w io.Writer, img image.Image, format sdk.ImageFormat, spec sdk.TransformSpec) error {
	lo, hi := 10, 92
	best := []byte(nil)
	for lo <= hi {
		mid := (lo + hi) / 2
		var buf bytes.Buffer
		s := spec
		s.Quality = mid
		s.TargetBytes = 0
		if err := encodeOnce(&buf, img, format, s); err != nil {
			return err
		}
		best = buf.Bytes()
		if int64(buf.Len()) <= int64(spec.TargetBytes) {
			hi = mid - 1 // 还能再小 → 继续压
		} else {
			lo = mid + 1
		}
	}
	if best == nil {
		return fmt.Errorf("压缩到目标体积失败:质量已到下限仍超出 %d 字节;建议同时缩小尺寸", spec.TargetBytes)
	}
	if int64(len(best)) > int64(spec.TargetBytes) {
		return fmt.Errorf("压到质量下限仍超出目标体积:目标 %d 字节,最小可达 %d 字节(要更小就缩小尺寸)",
			spec.TargetBytes, len(best))
	}
	_, err := w.Write(best)
	return err
}

// ── 几何变换 ────────────────────────────────────────────────────

// resizeImage 按 ResizeSpec 缩放。
func resizeImage(img image.Image, r *sdk.ResizeSpec) image.Image {
	mode := r.Mode
	if mode == "" {
		mode = "fit"
	}
	scaler := pickFilter(r.Filter)
	sw, sh := img.Bounds().Dx(), img.Bounds().Dy()
	if sw <= 0 || sh <= 0 {
		return img
	}
	var tw, th int
	switch mode {
	case "exact":
		tw, th = max(1, r.W), max(1, r.H)
	case "cover":
		// 等比缩放铺满包围盒后裁掉溢出部分
		scale := max(float64(r.W)/float64(sw), float64(r.H)/float64(sh))
		tw, th = max(1, int(float64(sw)*scale+0.5)), max(1, int(float64(sh)*scale+0.5))
	default: // fit
		scale := min(float64(r.W)/float64(sw), float64(r.H)/float64(sh))
		tw, th = max(1, int(float64(sw)*scale+0.5)), max(1, int(float64(sh)*scale+0.5))
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	scaler.Scale(dst, dst.Bounds(), img, img.Bounds(), drawOp, nil)
	if mode == "cover" && (tw > r.W || th > r.H) {
		// 铺满后裁掉溢出(cover 语义:先按较大缩放比铺满包围盒,再裁到框内)
		dst = image.NewRGBA(image.Rect(0, 0, r.W, r.H))
		scaler.Scale(dst, dst.Bounds(), img, img.Bounds(), drawOp, nil)
	}
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

// isAnimatedGIF 动画 GIF 取首帧(K3);只读文件头数帧,不解码整幅。
func isAnimatedGIF(path string) bool {
	fh, err := os.Open(path)
	if err != nil {
		return false
	}
	defer fh.Close()
	g, err := gif.DecodeAll(bufio.NewReader(fh))
	return err == nil && len(g.Image) > 1
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
