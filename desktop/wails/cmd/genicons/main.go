// genicons:从唯一图标源(desktop/icons/gah-icon-1024.png)生成壳要用的三份图标。
//
// 为什么需要它:壳不再用 Wails 自带的默认图标(托盘/窗口/Dock 都得是 gah 的),
// 而 macOS 菜单栏要的是**模板图**(纯黑 + alpha,由系统按明暗主题反色),直接拿彩色
// PNG 放上去在深色菜单栏里会糊成一团。
//
// 纯 Go、零依赖(CI 的 windows runner 也要能跑),缩放用最近邻 —— 32px 图标够用。
//
// 用法(改了图标再跑一次,产物入库):
//
//	go run ./desktop/wails/cmd/genicons
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// 输入:仓库里的图标源(1024×1024 PNG,透明背景)。
const srcPath = "desktop/icons/gah-icon-1024.png"

// 产物(入库,构建时 go:embed)。
var outputs = []struct {
	path string
	size int
	mode string // "app" 原样缩放;"tray" 彩色;"template" 纯黑+alpha
}{
	{"desktop/wails/assets/appicon.png", 256, "app"},
	{"desktop/wails/assets/tray.png", 32, "tray"},
	{"desktop/wails/assets/tray-template.png", 32, "template"},
}

func main() {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		fatal("读图标源失败: %v", err)
	}
	src, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		fatal("解析 PNG 失败: %v", err)
	}
	for _, o := range outputs {
		img := resize(src, o.size)
		if o.mode == "template" {
			img = toTemplate(img)
		}
		if err := writePNG(o.path, img); err != nil {
			fatal("写 %s 失败: %v", o.path, err)
		}
		fmt.Printf("%s %dx%d (%s)\n", o.path, o.size, o.size, o.mode)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "genicons: "+format+"\n", args...)
	os.Exit(1)
}

// resize 最近邻缩放(图标是几何图形,32px 下够清楚,且不引第三方)。
func resize(src image.Image, size int) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		sy := b.Min.Y + y*h/size
		for x := 0; x < size; x++ {
			sx := b.Min.X + x*w/size
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

// toTemplate 转模板图:RGB 归零,**alpha 用原图的 alpha 与暗度共同决定**。
//
// 只用 alpha 的话,浅色图形(白底图标)在菜单栏里会变成一整块实心方块;
// 用「不透明度 × 暗度」求出的遮罩才是原图的黑白轮廓。
func toTemplate(src *image.NRGBA) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bb, a := src.At(x, y).RGBA()
			// 感知亮度(0..65535),越暗越像「笔画」。
			lum := (299*uint64(r) + 587*uint64(g) + 114*uint64(bb)) / 1000
			ink := 65535 - lum
			alpha := uint32(a) * uint32(ink) / 65535
			dst.Set(x, y, color.NRGBA{A: uint8(alpha >> 8)})
		}
	}
	return dst
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
