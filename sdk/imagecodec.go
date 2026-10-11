// imagecodec.go:图像编解码的**宿主可插拔契约**(M2 seam)。
//
// 为什么要有这道缝:纯 Go 生态在图像上有硬边界 —— `image/jpeg` 不处理 EXIF 方向、
// 没有 WebP/HEIC/AVIF 的完整实现、48MP 大图解码进内存就是几百 MB。Android 侧用系统
// `ImageDecoder` 能解 HEIC/AVIF 并**边解边降采样**,这在纯 Go 里做不到。
// 于是把"怎么编解码"抽象成一个接口:桌面注册不注册都由内置纯 Go 实现兜底,Android 注册
// 原生实现即自动多出 HEIC/AVIF 解码与有损 WebP 编码。
//
// **路径进、路径出**(D1):跨实现只交换路径与规格,各自做文件 I/O —— 一张 10 MB 原图 +
// 200 MB 位图不该穿过 JNI 来回拷贝。
package sdk

import "sync"

// ImageFormat 图像格式(自述支持面用;空串 = 保持源格式)。
type ImageFormat string

const (
	FormatJPEG       ImageFormat = "jpeg"
	FormatPNG        ImageFormat = "png"
	FormatGIF        ImageFormat = "gif"
	FormatBMP        ImageFormat = "bmp"
	FormatTIFF       ImageFormat = "tiff"
	FormatWEBP       ImageFormat = "webp"       // 纯 Go 仅解码;编码靠宿主
	FormatWEBPLossy  ImageFormat = "webp-lossy" // 宿主专有:有损 WebP 编码
	FormatWEBPNoLoss ImageFormat = "webp-lossless"
	FormatHEIC       ImageFormat = "heic"
	FormatAVIF       ImageFormat = "avif"
)

// DefaultImageLimits 内置纯 Go 实现的硬上限(K2):48MP ≈ 200 MB RGBA,Go 无法边解边降采样,
// 与其 OOM 不如显式报错并告诉模型怎么办(宿主实现可边解边降采样,不受此限)。
const (
	DefaultMaxPixels = 30_000_000
	DefaultMaxDim    = 8000
)

// DecodeOpt 解码约束(宿主实现可据此选解码策略;纯 Go 实现超限即报错)。
type DecodeOpt struct {
	MaxPixels      int  // 像素上限(DefaultMaxPixels)
	MaxDim         int  // 长边上限(DefaultMaxDim)
	AllowDownscale bool // true = 允许实现方边解边降采样(宿主原生实现),false = 超限即报错
}

// ResizeSpec 缩放规格(与 TransformSpec.MaxDim 互斥)。
type ResizeSpec struct {
	W, H   int    // 目标尺寸(Mode=exact 时严格按此;fit/cover 时作为包围盒)
	Mode   string // fit(等比缩放进包围盒) | cover(等比缩放铺满后裁) | exact(强制尺寸)
	Filter string // nearest | bilinear | catmullrom(默认 catmullrom)
}

// CropSpec 裁剪区域;坐标以**按 EXIF 摆正后**的图像为准(K1)。
type CropSpec struct{ X, Y, W, H int }

// TransformSpec 一次变换的全部规格(工具层填,实现层执行)。
type TransformSpec struct {
	Format      ImageFormat // 空 = 保持源格式
	Quality     int         // 1..100(有损格式;0 = 用实现默认值)
	Lossless    bool        // PNG/WebP 无损
	MaxDim      int         // 等比缩放到长边 ≤ MaxDim(与 Resize 互斥)
	Resize      *ResizeSpec
	Crop        *CropSpec
	Rotate      int  // 0/90/180/270(顺时针)
	AutoOrient  bool // 默认 true:按 EXIF Orientation 摆正并**烘焙**,输出不再带该字段
	StripAll    bool // 默认 true:剥离 EXIF/GPS 等全部元数据(K4:手机照片带定位)
	TargetBytes int  // >0 时按目标体积迭代质量(仅 jpeg/webp 有损有意义)
}

// ImageInfo 不含像素的图像概要(工具的 info 动作直接回它)。
type ImageInfo struct {
	Path      string      `json:"path"`
	Format    ImageFormat `json:"format"`
	Width     int         `json:"width"` // 摆正后的视觉宽
	Height    int         `json:"height"`
	Bytes     int64       `json:"bytes"`
	Animated  bool        `json:"animated,omitempty"`   // 动画 GIF/APNG:内置实现只取首帧
	MultiPage bool        `json:"multi_page,omitempty"` // 多页 TIFF / HEIC 多图:取主图
	Alpha     bool        `json:"alpha,omitempty"`
	EXIF      EXIFSummary `json:"exif"`
}

// EXIFSummary EXIF 概要(工具给模型看的最小集;不复制任何 GPS 坐标值 —— 只报"有没有")。
type EXIFSummary struct {
	Present     bool   `json:"present"`
	Orientation int    `json:"orientation,omitempty"` // 1..8;非 1 表示需要摆正
	CameraMake  string `json:"camera_make,omitempty"`
	CameraModel string `json:"camera_model,omitempty"`
	HasGPS      bool   `json:"has_gps"`            // 只报存在性:**坐标值一律不外传**
	Stripped    bool   `json:"stripped,omitempty"` // 本次输出是否已剥离元数据
}

// CodecCaps 实现自述能力(选路与报错文案都靠它 —— 不支持就说清楚,不许静默降级)。
type CodecCaps struct {
	Decode       []ImageFormat
	Encode       []ImageFormat
	MaxPixels    int
	CanDownscale bool // 能否边解边降采样(宿主原生实现 = true)
}

// ImageCodec 编解码实现。**注册失败必须显式**(否则用户以为 HEIC 能用而它静默失败)。
type ImageCodec interface {
	Caps() CodecCaps
	Info(path string) (ImageInfo, error)                 // 不含像素
	Transform(src, dst string, spec TransformSpec) error // 路径进、路径出
}

var (
	imageCodecMu sync.RWMutex
	hostCodec    ImageCodec
)

// RegisterImageCodec 宿主注册实现(Android 壳侧经 JNI 注册原生编解码器)。
//
// 单值:后注册覆盖前值(nil = 注销)。插件侧读取顺序:ctx 上的 system.imageCodec 服务 →
// 本注册表 → 内置纯 Go 实现(桌面不注册 = 纯 Go)。
func RegisterImageCodec(c ImageCodec) {
	imageCodecMu.Lock()
	defer imageCodecMu.Unlock()
	hostCodec = c
}

// RegisteredImageCodec 宿主已注册的实现;未注册返回 nil(调用方回落内置实现)。
func RegisteredImageCodec() ImageCodec {
	imageCodecMu.RLock()
	defer imageCodecMu.RUnlock()
	return hostCodec
}
