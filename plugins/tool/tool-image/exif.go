// exif.go:JPEG(与 TIFF)的 EXIF **最小读取**。
//
// 只取三件事:Orientation(K1 摆正必需)、相机 make/model(给模型看的概要)、有没有 GPS。
//
// 为什么自己写而不是引一个 EXIF 库:要的字段就这几个,而引库意味着又多一个依赖与它的传递依赖;
// 更重要的是**隐私纪律** —— 这里只回答"有没有定位",坐标值一律不读进内存、更不外传(K4)。
//
// 读不到/损坏一律返回零值:EXIF 坏了不该让整次变换失败。
package toolimage

import (
	"encoding/binary"
	"os"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// exifData 内部读取结果(外部只暴露 sdk.EXIFSummary)。
type exifData struct {
	Orientation int
	Make        string
	Model       string
	HasGPS      bool
	P3          bool
}

// summary 转对外口径:GPS 只报存在性,坐标值从不进内存(K4)。
func (e exifData) summary() sdk.EXIFSummary {
	return sdk.EXIFSummary{
		Present:     e.Orientation > 0 || e.Make != "" || e.Model != "" || e.HasGPS,
		Orientation: e.Orientation, CameraMake: e.Make, CameraModel: e.Model, HasGPS: e.HasGPS,
	}
}

// readEXIF 读 <path> 的 EXIF 概要;失败返回零值(不报错)。
func readEXIF(path string) exifData {
	fh, err := os.Open(path)
	if err != nil {
		return exifData{}
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil || st.Size() <= 0 {
		return exifData{}
	}
	// 只读前 256 KiB:EXIF 段在文件头附近,而整张 48MP 图不该为读 EXIF 进内存。
	const maxHead = 256 << 10
	n := int(min(st.Size(), maxHead))
	buf := make([]byte, n)
	read, _ := readFull(fh, buf)
	buf = buf[:read]
	return parseEXIF(buf)
}

func readFull(fh *os.File, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := fh.Read(buf[total:])
		total += n
		if err != nil || n == 0 {
			return total, err
		}
	}
	return total, nil
}

// parseEXIF 从 JPEG APP1/TIFF 里取字段。
func parseEXIF(buf []byte) exifData {
	var out exifData
	tiff := findTIFF(buf)
	if len(tiff) < 8 {
		return out
	}
	bo := tiff[0] // 'I'(小端) 或 'M'(大端)
	if bo != 'I' && bo != 'M' {
		return out
	}
	var u16 func(int) int
	var u32 func(int) int
	if bo == 'I' {
		u16 = func(i int) int {
			if i+2 > len(tiff) {
				return -1
			}
			return int(binary.LittleEndian.Uint16(tiff[i:]))
		}
		u32 = func(i int) int {
			if i+4 > len(tiff) {
				return -1
			}
			return int(binary.LittleEndian.Uint32(tiff[i:]))
		}
	} else {
		u16 = func(i int) int {
			if i+2 > len(tiff) {
				return -1
			}
			return int(binary.BigEndian.Uint16(tiff[i:]))
		}
		u32 = func(i int) int {
			if i+4 > len(tiff) {
				return -1
			}
			return int(binary.BigEndian.Uint32(tiff[i:]))
		}
	}
	if u16(2) != 42 { // TIFF magic
		return out
	}
	ifd := u32(4)
	if ifd <= 0 || ifd+2 > len(tiff) {
		return out
	}
	count := u16(ifd)
	for i := 0; i < count; i++ {
		ent := ifd + 2 + i*12
		if ent+12 > len(tiff) {
			return out
		}
		tag := u16(ent)
		typ := u16(ent + 2)
		val := u32(ent + 8) // 4 字节内联值(本文件只关心 ≤4 字节的类型)
		switch tag {
		case 0x0112: // Orientation
			if v := u16(ent + 8); v >= 1 && v <= 8 {
				out.Orientation = v
			}
		case 0x010F: // Make(ASCII)
			out.Make = readASCII(tiff, typ, val, u32)
		case 0x0110: // Model
			out.Model = readASCII(tiff, typ, val, u32)
		case 0x8825: // GPS IFD 指针:只记存在性(**不跟进指针**,坐标值一律不进内存,K4)
			if val > 0 {
				out.HasGPS = true
			}
		case 0xA001: // ColorSpace(1 = sRGB);0xFFFF 未定义常被相机用于 P3/JPEG-RGB
			_ = typ
		}
	}
	// 宽色域判定:ICC profile(APP2)或 MPF(CMM)标记 → 提示会偏色(Go 无 ICC 转换)。
	out.P3 = hasICCProfile(buf)
	return out
}

// readASCII 读 ASCII 型 tag 的字符串(值内联在 4 字节里时取前若干字符)。
func readASCII(tiff []byte, typ, val int, _ func(int) int) string {
	if typ != 2 || val <= 0 { // ASCII 型,值偏移必须在正数
		return ""
	}
	if val >= len(tiff) {
		// 4 字节内联值:打印型相机常把 make/model 前缀塞在这里
		return trimNUL(string(tiff[min(val, len(tiff)):min(val+4, len(tiff))]))
	}
	end := val
	for end < len(tiff) && tiff[end] != 0 && end-val < 64 {
		end++
	}
	return trimNUL(string(tiff[val:end]))
}

func trimNUL(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return s[:i]
		}
	}
	return s
}

// findTIFF 找 TIFF 头:TIFF 文件从偏移 0 开始;JPEG 走 APP1("Exif\0\0" 之后)。
func findTIFF(buf []byte) []byte {
	if len(buf) > 4 && (buf[0] == 'I' && buf[1] == 'I' || buf[0] == 'M' && buf[1] == 'M') {
		if u := le16(buf, 2); u == 42 || be16(buf, 2) == 42 {
			return buf
		}
	}
	for i := 0; i+4 < len(buf); i++ {
		if buf[i] != 0xFF || buf[i+1] != 0xE1 { // APP1
			continue
		}
		if i+10 <= len(buf) && string(buf[i+4:i+10]) == "Exif\x00\x00" {
			return buf[i+10:]
		}
	}
	return nil
}

// hasICCProfile APP2 段里有 "ICC_PROFILE" → 源带色彩管理(转 sRGB 会偏色,须提示)。
func hasICCProfile(buf []byte) bool {
	for i := 0; i+15 <= len(buf); i++ {
		if buf[i] == 0xFF && buf[i+1] == 0xE2 && string(buf[i+4:i+15]) == "ICC_PROFILE" {
			return true
		}
	}
	return false
}

func le16(b []byte, i int) int {
	if i+2 > len(b) {
		return -1
	}
	return int(binary.LittleEndian.Uint16(b[i:]))
}

func be16(b []byte, i int) int {
	if i+2 > len(b) {
		return -1
	}
	return int(binary.BigEndian.Uint16(b[i:]))
}
