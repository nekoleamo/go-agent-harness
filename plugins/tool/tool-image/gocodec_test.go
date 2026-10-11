package toolimage

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// ── 夹具 ────────────────────────────────────────────────────────

// solidImage 生成 w×h 的双色渐变图(便于断言像素位置与旋转/裁剪结果)。
func solidImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / max(1, w-1)), G: uint8(y * 255 / max(1, h-1)), B: 200, A: 255})
		}
	}
	return img
}

// writeJPEG 落一张带 EXIF 的 JPEG(带可选 EXIF 段:orientation + GPS)。
//
// 注意段序:JPEG 必须是 SOI 在最前,所以走"先编码到内存、再把 APP1 段插到 SOI 之后"
// —— 直接在 jpeg.Encode 之前写 APP1 会得到 SOI、APP1、SOI 的畸形流,解码器报 unknown marker。
func writeJPEG(t *testing.T, path string, img image.Image, exif []byte) {
	t.Helper()
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, img, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatal(err)
	}
	body := raw.Bytes()
	out := make([]byte, 0, len(body)+len(exif)+16)
	out = append(out, body[0], body[1]) // SOI
	if len(exif) > 0 {
		seg := append([]byte{0xFF, 0xE1, 0, 0, 'E', 'x', 'i', 'f', 0, 0}, exif...)
		n := len(seg) - 2 // 长度字段含自身两字节
		seg[2], seg[3] = byte(n>>8), byte(n)
		out = append(out, seg...)
	}
	out = append(out, body[2:]...)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// exifBlock 造一个最小 TIFF/EXIF 块(Orientation + GPS IFD 指针,小端)。
func exifBlock(orientation int, withGPS bool) []byte {
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0} // 头部:字节序/魔数/IFD0 偏移=8
	ifd := tiff[4:]
	_ = ifd
	entries := []byte{}
	// tag(2) type(2) count(4) value(4):Orientation(0x0112) 值内联在前两字节
	orientEntry := []byte{0x12, 0x01, 0x03, 0x00, 1, 0, 0, 0, byte(orientation), 0, 0, 0}
	entries = append(entries, orientEntry...)
	n := 1
	if withGPS {
		// GPSInfoIFDPointer(0x8825):LONG 型,值 = 8+2+2*12+4 = 38
		gpsEntry := []byte{0x25, 0x88, 0x04, 0x00, 1, 0, 0, 0, 38, 0, 0, 0}
		entries = append(entries, gpsEntry...)
		n = 2
	}
	out := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, byte(n), 0}
	out = append(out, entries...)
	return out
}

func mustWritePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if err := png.Encode(fh, img); err != nil {
		t.Fatal(err)
	}
}

func decodeAny(t *testing.T, path string) image.Image {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	img, _, err := image.Decode(fh)
	if err != nil {
		t.Fatalf("解码 %s 失败: %v", path, err)
	}
	return img
}

// ── K1:EXIF 方向 ────────────────────────────────────────────────

// TestAutoOrientBakesEXIFOrientation K1:竖拍照片(Orientation=6)在 transform 后
// 像素必须已经摆正,且输出**不带 EXIF**(方向被烘焙)。
func TestAutoOrientBakesEXIFOrientation(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "portrait.jpg")
	// 源像素是 20×10 的横图,EXIF 说"顺时针转 90°"⇒ 摆正后应是 10×20 的竖图
	writeJPEG(t, src, solidImage(20, 10), exifBlock(6, true))

	c := NewGoCodec()
	info, err := c.Info(src)
	if err != nil {
		t.Fatal(err)
	}
	if info.EXIF.Orientation != 6 {
		t.Fatalf("Orientation 应读出 6,得 %d", info.EXIF.Orientation)
	}
	if !info.EXIF.HasGPS {
		t.Fatal("应报出存在 GPS(K4:只报存在性)")
	}
	if info.Width != 20 || info.Height != 10 { // Info 报的是像素尺寸,方向另字段
		t.Fatalf("Info 尺寸 = %dx%d, want 20x10", info.Width, info.Height)
	}

	dst := filepath.Join(dir, "out.png")
	if err := c.Transform(src, dst, sdk.TransformSpec{AutoOrient: true, StripAll: true}); err != nil {
		t.Fatal(err)
	}
	out := decodeAny(t, dst)
	if out.Bounds().Dx() != 10 || out.Bounds().Dy() != 20 {
		t.Fatalf("摆正后应为 10x20,得 %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
	// 输出不带元数据 ⇒ 再读一次 EXIF 应为空(K4)
	after, err := c.Info(dst)
	if err != nil {
		t.Fatal(err)
	}
	if after.EXIF.Orientation != 0 || after.EXIF.HasGPS {
		t.Fatalf("输出不该带 EXIF/GPS: %+v", after.EXIF)
	}
}

// TestCropUsesOrientedCoordinates K1 的另一半:裁剪坐标按**摆正后**的图像算。
func TestCropUsesOrientedCoordinates(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "p.jpg")
	writeJPEG(t, src, solidImage(20, 10), exifBlock(6, false))
	c := NewGoCodec()
	dst := filepath.Join(dir, "crop.png")
	// 摆正后是 10×20;裁左上一块 4×6
	if err := c.Transform(src, dst, sdk.TransformSpec{
		AutoOrient: true, StripAll: true,
		Crop: &sdk.CropSpec{X: 0, Y: 0, W: 4, H: 6},
	}); err != nil {
		t.Fatal(err)
	}
	out := decodeAny(t, dst)
	if out.Bounds().Dx() != 4 || out.Bounds().Dy() != 6 {
		t.Fatalf("裁剪结果应 4x6,得 %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
	// 坐标基于摆正后 ⇒ 超出摆正尺寸的范围必须报错(而不是静默裁到边)
	if err := c.Transform(src, filepath.Join(dir, "bad.png"), sdk.TransformSpec{
		AutoOrient: true, Crop: &sdk.CropSpec{X: 0, Y: 0, W: 40, H: 6},
	}); err == nil || !strings.Contains(err.Error(), "超出图像范围") {
		t.Fatalf("超范围裁剪应显式报错,得 %v", err)
	}
}

func TestApplyOrientationAllCases(t *testing.T) {
	// 2..8 都要真的换维度或换像素,不能只有 6 有效
	src := solidImage(6, 3)
	for _, tc := range []struct {
		ori      int
		wantW    int
		wantH    int
		swapLike bool
	}{
		{2, 6, 3, false}, {3, 6, 3, false}, {4, 6, 3, false},
		{5, 3, 6, true}, {6, 3, 6, true}, {7, 3, 6, true}, {8, 3, 6, true},
	} {
		out, applied := applyOrientation(src, tc.ori)
		if applied != tc.ori {
			t.Fatalf("orientation=%d 应被应用,得 %d", tc.ori, applied)
		}
		if out.Bounds().Dx() != tc.wantW || out.Bounds().Dy() != tc.wantH {
			t.Fatalf("orientation=%d → %dx%d, want %dx%d", tc.ori,
				out.Bounds().Dx(), out.Bounds().Dy(), tc.wantW, tc.wantH)
		}
	}
	// 1 与非法值 = 不动
	if out, applied := applyOrientation(src, 1); applied != 1 || out.Bounds().Dx() != 6 {
		t.Fatal("orientation=1 应原样返回")
	}
}

// ── K2:大图上限 ───────────────────────────────────────────────

func TestTooBigRejectedBeforeDecode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "huge.bmp")
	// 只写 BMP 头即可(超限判定发生在解码之前 ⇒ 不该真去分配 12000×8000)
	writeBMPHeaderOnly(t, src, 12000, 8000)
	c := NewGoCodec()
	err := c.Transform(src, filepath.Join(dir, "out.png"), sdk.TransformSpec{AutoOrient: true})
	if err == nil {
		t.Fatal("超限应报错")
	}
	if !strings.Contains(err.Error(), "过大") || !strings.Contains(err.Error(), "resize") {
		t.Fatalf("错误文案要给出路(先缩小再处理): %v", err)
	}
	// Info 仍应秒回(不解码像素)
	if _, err := c.Info(src); err != nil {
		t.Fatalf("info 不该因超限失败: %v", err)
	}
}

// ── K3:动画取首帧 ───────────────────────────────────────────────

func TestAnimatedGIFTakesFirstFrame(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "anim.gif")
	writeAnimatedGIF(t, src, 2)
	c := NewGoCodec()
	info, err := c.Info(src)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Animated {
		t.Fatal("应识别出动画 GIF(K3)")
	}
	dst := filepath.Join(dir, "out.png")
	if err := c.Transform(src, dst, sdk.TransformSpec{AutoOrient: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("取首帧后应写出结果: %v", err)
	}
}

// ── 转换 / 缩放 / 旋转 / 压缩 ───────────────────────────────────

func TestConvertFormatsAndUnsupported(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.png")
	mustWritePNG(t, src, solidImage(8, 8))
	c := NewGoCodec()
	for _, f := range []sdk.ImageFormat{sdk.FormatJPEG, sdk.FormatPNG, sdk.FormatGIF, sdk.FormatBMP, sdk.FormatTIFF} {
		dst := filepath.Join(dir, "out."+string(f))
		if err := c.Transform(src, dst, sdk.TransformSpec{Format: f, AutoOrient: true, StripAll: true}); err != nil {
			t.Fatalf("转 %s 失败: %v", f, err)
		}
		if _, err := os.Stat(dst); err != nil {
			t.Fatalf("%s 未落盘: %v", f, err)
		}
	}
	// 不支持的编码必须显式报错并说明出路
	err := c.Transform(src, filepath.Join(dir, "out.webp"), sdk.TransformSpec{Format: sdk.FormatWEBP})
	if err == nil || !strings.Contains(err.Error(), "宿主") {
		t.Fatalf("WebP 编码(纯 Go)应显式报错并指向宿主实现,得 %v", err)
	}
	// HEIC/AVIF 解码同理(文件得真的在,才能走到"格式不支持"那一层)
	heic := filepath.Join(dir, "x.heic")
	if err := os.WriteFile(heic, append([]byte{0, 0, 0, 0x20}, []byte("ftypheic")...), 0o644); err != nil {
		t.Fatal(err)
	}
	err = c.Transform(heic, filepath.Join(dir, "y.png"), sdk.TransformSpec{})
	if err == nil || !strings.Contains(err.Error(), "宿主") {
		t.Fatalf("HEIC 应显式报错并指向宿主实现,得 %v", err)
	}
	// 格式保持:不给 format 时按 dst 扩展名
	dst := filepath.Join(dir, "keep.jpg")
	if err := c.Transform(src, dst, sdk.TransformSpec{AutoOrient: true}); err != nil {
		t.Fatal(err)
	}
	if format, _ := sniffFormat(dst); format != sdk.FormatJPEG {
		t.Fatalf("缺省格式应跟 dst 扩展名(jpeg),得 %s", format)
	}
}

func TestResizeCropRotate(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.png")
	mustWritePNG(t, src, solidImage(100, 50))
	c := NewGoCodec()
	// fit:等比缩放进包围盒
	dst := filepath.Join(dir, "fit.png")
	if err := c.Transform(src, dst, sdk.TransformSpec{
		AutoOrient: true, Resize: &sdk.ResizeSpec{W: 40, H: 40, Mode: "fit"},
	}); err != nil {
		t.Fatal(err)
	}
	out := decodeAny(t, dst)
	if out.Bounds().Dx() != 40 || out.Bounds().Dy() != 20 {
		t.Fatalf("fit 结果应为 40x20,得 %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
	// exact:严格尺寸
	dst = filepath.Join(dir, "exact.png")
	if err := c.Transform(src, dst, sdk.TransformSpec{
		AutoOrient: true, Resize: &sdk.ResizeSpec{W: 33, H: 21, Mode: "exact"},
	}); err != nil {
		t.Fatal(err)
	}
	out = decodeAny(t, dst)
	if out.Bounds().Dx() != 33 || out.Bounds().Dy() != 21 {
		t.Fatalf("exact 结果应为 33x21,得 %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
	// max_dim:长边上限
	dst = filepath.Join(dir, "maxdim.png")
	if err := c.Transform(src, dst, sdk.TransformSpec{AutoOrient: true, MaxDim: 25}); err != nil {
		t.Fatal(err)
	}
	out = decodeAny(t, dst)
	if max(out.Bounds().Dx(), out.Bounds().Dy()) != 25 {
		t.Fatalf("max_dim=25 后长边应为 25,得 %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
	// 旋转 90:宽高互换
	dst = filepath.Join(dir, "rot.png")
	if err := c.Transform(src, dst, sdk.TransformSpec{AutoOrient: true, Rotate: 90}); err != nil {
		t.Fatal(err)
	}
	out = decodeAny(t, dst)
	if out.Bounds().Dx() != 50 || out.Bounds().Dy() != 100 {
		t.Fatalf("旋转 90 后应为 50x100,得 %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
	// 非法旋转角
	if err := c.Transform(src, filepath.Join(dir, "bad.png"), sdk.TransformSpec{Rotate: 45}); err == nil {
		t.Fatal("非法旋转角应报错")
	}
	// crop 与 resize 互斥
	if err := c.Transform(src, filepath.Join(dir, "both.png"), sdk.TransformSpec{
		Crop: &sdk.CropSpec{W: 10, H: 10}, Resize: &sdk.ResizeSpec{W: 5, H: 5},
	}); err == nil || !strings.Contains(err.Error(), "互斥") {
		t.Fatalf("crop+resize 互斥应报错,得 %v", err)
	}
}

// TestCompressToTargetBytes 压到目标体积:达成或有可行动的失败(±10%)。
func TestCompressToTargetBytes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "big.png")
	img := image.NewRGBA(image.Rect(0, 0, 600, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 600; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: uint8((x * y) % 256), A: 255})
		}
	}
	mustWritePNG(t, src, img)
	c := NewGoCodec()
	dst := filepath.Join(dir, "small.jpg")
	target := 40 << 10
	if err := c.Transform(src, dst, sdk.TransformSpec{Format: sdk.FormatJPEG, AutoOrient: true, TargetBytes: target}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > int64(target) {
		t.Fatalf("压到目标体积失败: %d > %d", st.Size(), target)
	}
	if st.Size() < int64(target)*9/10 {
		t.Logf("压得比目标还小(%d < %d*0.9),可接受但不够贴近", st.Size(), target)
	}
	// 无损格式上要目标体积 ⇒ 明确说不适用
	if err := c.Transform(src, filepath.Join(dir, "x.png"), sdk.TransformSpec{
		Format: sdk.FormatPNG, TargetBytes: target,
	}); err == nil || !strings.Contains(err.Error(), "resize") {
		t.Fatalf("PNG 上 target_bytes 应说明改用 resize,得 %v", err)
	}
}

// TestWriteIsAtomic 目标位置不留半截文件,也不留临时文件。
func TestWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.png")
	mustWritePNG(t, src, solidImage(8, 8))
	c := NewGoCodec()
	// 目标不可写(用一个不存在的父目录)⇒ 失败,且不产生文件
	bad := filepath.Join(dir, "nodir", "x.png")
	if err := c.Transform(src, bad, sdk.TransformSpec{AutoOrient: true}); err == nil {
		t.Fatal("写不进去应报错")
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatalf("失败时不该留下文件: %v", err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), "gah-tmp") {
			t.Fatalf("不该留下临时文件: %s", e.Name())
		}
	}
}

func TestSniffFormatIgnoresExtension(t *testing.T) {
	dir := t.TempDir()
	// 内容是 PNG,名字却是 .jpg:模型经常这么干,不能信扩展名
	p := filepath.Join(dir, "liar.jpg")
	mustWritePNG(t, p, solidImage(4, 4))
	got, err := sniffFormat(p)
	if err != nil || got != sdk.FormatPNG {
		t.Fatalf("应按内容判定为 png,得 %s(%v)", got, err)
	}
}

func TestCapsReportSupportFace(t *testing.T) {
	c := NewGoCodec()
	caps := c.Caps()
	if !c.SupportsDecode(sdk.FormatWEBP) {
		t.Fatal("应支持 WebP 解码")
	}
	if c.SupportsEncode(sdk.FormatWEBP) {
		t.Fatal("纯 Go 不该声称能编码 WebP")
	}
	if c.SupportsEncode(sdk.FormatHEIC) || c.SupportsDecode(sdk.FormatHEIC) {
		t.Fatal("纯 Go 不该声称支持 HEIC")
	}
	if caps.CanDownscale {
		t.Fatal("纯 Go 不能边解边降采样")
	}
	if caps.MaxPixels != sdk.DefaultMaxPixels {
		t.Fatalf("MaxPixels = %d, want %d", caps.MaxPixels, sdk.DefaultMaxPixels)
	}
}

func TestParseEXIFNoPanicOnGarbage(t *testing.T) {
	// 垃圾/截断的 EXIF 不该 panic(单测量的是"不会崩")
	for _, b := range [][]byte{
		nil, {0xFF, 0xE1, 0x00}, []byte("II*\x00\xff\xff\xff\xff"),
		append([]byte{0xFF, 0xE1, 0x00, 0x08}, []byte("Exif\x00\x00")...),
		bytes.Repeat([]byte{0x41}, 300),
	} {
		_ = parseEXIF(b)
	}
	if d := parseEXIF(exifBlock(8, true)); d.Orientation != 8 || !d.HasGPS {
		t.Fatalf("解析应得到 Orientation=8 + HasGPS,得 %+v", d)
	}
}

// writeBMPHeaderOnly 只写 BMP 文件头 + 信息头(声明尺寸、不给像素数据)。
// 选 BMP 是因为它的头能被 image.DecodeConfig 直接读出尺寸 —— 于是"超限在解码前就被拒"
// 这条路径可以真跑;写一张 12000×8000 的真图来测同一件事只会把测试机撑爆。
func writeBMPHeaderOnly(t *testing.T, path string, w, h int) {
	t.Helper()
	var b bytes.Buffer
	b.Write([]byte{'B', 'M'})
	b.Write(le32(54)) // 文件大小
	b.Write(le32(0))  // 保留
	b.Write(le32(54)) // 像素数据偏移
	b.Write(le32(40)) // BITMAPINFOHEADER 大小
	b.Write(le32(w))
	b.Write(le32(h))
	b.Write([]byte{1, 0})  // 平面数
	b.Write([]byte{24, 0}) // 位深
	b.Write(le32(0))       // 压缩方式
	b.Write(le32(0))       // 图像大小
	b.Write(le32(2835))
	b.Write(le32(2835))
	b.Write(le32(0))
	b.Write(le32(0))
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func le32(v int) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

// writeAnimatedGIF 造多帧 GIF(K3 夹具)。
func writeAnimatedGIF(t *testing.T, path string, frames int) {
	t.Helper()
	var buf bytes.Buffer
	g := &gif.GIF{}
	for f := 0; f < frames; f++ {
		pal := image.NewPaletted(image.Rect(0, 0, 8, 8), []color.Color{color.Black, color.White})
		for i := range pal.Pix {
			if (i+f)%2 == 0 {
				pal.Pix[i] = 1
			}
		}
		g.Image = append(g.Image, pal)
		g.Delay = append(g.Delay, 10)
	}
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
