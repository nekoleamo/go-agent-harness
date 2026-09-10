// 出站媒体契约(MED-1 / E-B 接口层;入站媒体工具见 media.go):通道实现可选能力 `MediaSender`,
// 由桥的产物登记账本(artifact.go)驱动——**通道永不接受任意路径**,只接已登记产物。
//
// 与 `TypingAware` 同型:可选能力经类型断言发现,未实现 = 显式报「该渠道不支持出站文件」,
// 不改动既有 `Transport`(Mock 与另一通道零改动)。
package im

import "context"

// MediaKind 出站媒体类型(与各通道 file_type / media_type 对齐的最小集合)。
type MediaKind string

const (
	MediaImage MediaKind = "image" // QQ file_type=1 / iLink media_type=1
	MediaVideo MediaKind = "video" // 2 / 2
	MediaVoice MediaKind = "voice" // 3 / 4(协议支持,iLink 官方实现无稳定发送 helper)
	MediaFile  MediaKind = "file"  // 4 / 3
)

// MediaPayload 一份**已登记**产物(仅 Bridge.RegisterArtifact 产出)。
// 通道侧应校验 ArtifactID 非空且 Path 可读;不得凭任意路径发送。
type MediaPayload struct {
	ArtifactID string
	Kind       MediaKind
	Path       string // 绝对路径(工作区内;登记时已 realpath 归一)
	Name       string // 对外文件名(通道展示用;非宿主绝对路径)
	Mime       string
	Size       int64
	// TimeUnixNano 源文件 mtime(通道用于 file_info 缓存键;登记时确定,发送前已复核一致性)。
	TimeUnixNano int64
}

// MediaSender 可选能力:通道支持出站媒体。
// 实现方负责平台差异(QQ 分片上传 + msg_type=7;iLink getuploadurl + AES 加密 + CDN 上传 + 媒体项),
// 并复用自身预算层/配额/delivery ledger(无旁路);失败原样返回错误(桥负责还原登记条目)。
type MediaSender interface {
	SendMedia(ctx context.Context, to Route, m MediaPayload) error
}
