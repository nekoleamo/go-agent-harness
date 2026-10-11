package sdk

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// ImageCodec seam 的契约测试:注册表语义 + 结构字段的 JSON 口径。

// fakeCodec 假实现。
type fakeCodec struct{ name string }

func (f *fakeCodec) Caps() CodecCaps {
	return CodecCaps{
		Decode: []ImageFormat{FormatHEIC, FormatAVIF}, Encode: []ImageFormat{FormatWEBPLossy},
		MaxPixels: 200_000_000, CanDownscale: true,
	}
}

func (f *fakeCodec) Info(string) (ImageInfo, error) {
	return ImageInfo{Path: "/x", Format: FormatHEIC, Width: 4032, Height: 3024}, nil
}

func (f *fakeCodec) Transform(src, dst string, _ TransformSpec) error { return nil }

func TestRegisteredImageCodecLifecycle(t *testing.T) {
	t.Cleanup(func() { RegisterImageCodec(nil) })
	if RegisteredImageCodec() != nil {
		t.Fatal("初始应无宿主实现(桌面 = 内置纯 Go)")
	}
	a, b := &fakeCodec{name: "a"}, &fakeCodec{name: "b"}
	RegisterImageCodec(a)
	if got := RegisteredImageCodec(); got != ImageCodec(a) {
		t.Fatalf("注册后应可读回: %v", got)
	}
	RegisterImageCodec(b) // 单值:后注册覆盖
	if got := RegisteredImageCodec(); got != ImageCodec(b) {
		t.Fatalf("后注册应覆盖前值: %v", got)
	}
	RegisterImageCodec(nil) // 注销
	if RegisteredImageCodec() != nil {
		t.Fatal("注销后应为 nil")
	}
}

// 并发注册/读取不得 data race(工具可能在多个回合并发里读)。
func TestRegisteredImageCodecConcurrent(t *testing.T) {
	t.Cleanup(func() { RegisterImageCodec(nil) })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); RegisterImageCodec(&fakeCodec{}) }()
		go func() { defer wg.Done(); _ = RegisteredImageCodec() }()
	}
	wg.Wait()
}

// ImageInfo/EXIFSummary 的 JSON 字段名是对外契约(web 产物栏与工具结果都读它):
// 改名会让历史会话记录与前端同时失配。
func TestImageInfoJSONShape(t *testing.T) {
	info := ImageInfo{
		Path: "/a.jpg", Format: FormatJPEG, Width: 10, Height: 20, Bytes: 1234,
		Animated: true, MultiPage: false, Alpha: true,
		EXIF: EXIFSummary{Present: true, Orientation: 6, CameraMake: "A", CameraModel: "B",
			HasGPS: true, Stripped: true},
	}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"path"`, `"format"`, `"width"`, `"height"`, `"bytes"`,
		`"animated"`, `"alpha"`, `"exif"`, `"orientation"`, `"has_gps"`, `"stripped"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("JSON 缺字段 %s: %s", k, b)
		}
	}
	// multi_page=false 时省略(omitempty):不为 false 也占位
	if strings.Contains(string(b), `"multi_page"`) {
		t.Fatalf("false 的 multi_page 应省略: %s", b)
	}
	var back ImageInfo
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.EXIF.Orientation != 6 || !back.EXIF.HasGPS || back.Format != FormatJPEG {
		t.Fatalf("往返后字段丢了: %+v", back)
	}
}

func TestDefaultImageLimitsAreTheDocumentedOnes(t *testing.T) {
	// K2 的硬上限是被文档/工具描述引用的事实,改动会让那些文案说谎
	if DefaultMaxPixels != 30_000_000 {
		t.Fatalf("DefaultMaxPixels = %d, want 30000000", DefaultMaxPixels)
	}
	if DefaultMaxDim != 8000 {
		t.Fatalf("DefaultMaxDim = %d, want 8000", DefaultMaxDim)
	}
}

// TransformSpec 的零值语义:零值必须是"什么都不做"(AutoOrient/StripAll 由调用方显式置真)。
func TestTransformSpecZeroValueIsNoop(t *testing.T) {
	var s TransformSpec
	if s.Format != "" || s.Resize != nil || s.Crop != nil || s.Rotate != 0 ||
		s.TargetBytes != 0 || s.AutoOrient || s.StripAll || s.Lossless {
		t.Fatalf("TransformSpec 零值应是什么都不做: %+v", s)
	}
}

// 契约面刻意不含 DecodeOpt(见 imagecodec.go 的说明):接口没有任何方法能收解码参数,
// 加一个传不进去的结构只会让人以为"这里有旋钮可调"。这条用例钉住"契约面只有三方法"。
func TestImageCodecContractSurface(t *testing.T) {
	// 只有 Caps/Info/Transform 三个方法:实现方(Android 壳)照这三个就能接上,
	// 多一个方法就多一处"壳侧忘了实现"的失败面。
	var c ImageCodec = &fakeCodec{}
	if c.Caps().CanDownscale != true {
		t.Fatal("caps 失真")
	}
	if _, err := c.Info("x"); err != nil {
		t.Fatal(err)
	}
	if err := c.Transform("a", "b", TransformSpec{}); err != nil {
		t.Fatal(err)
	}
}
