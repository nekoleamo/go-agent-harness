// P4 平台匹配:embed 的外部插件产物必须与当前构建平台一致(否则首启释放的
// 插件二进制无法在目标机执行——发行产物平台匹配回归护栏)。
package embed

import (
	"encoding/binary"
	"io"
	"io/fs"
	"runtime"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// binArch 读取可执行文件头部,识别平台与架构(ELF/Mach-O/PE 魔数)。
func binArch(raw []byte) (osName, arch string) {
	if len(raw) >= 2 && raw[0] == 'M' && raw[1] == 'Z' {
		// PE: DOS e_lfanew(offset 0x3C)→ PE 头 → machine(offset +4)
		if len(raw) >= 4+64+4+2 {
			peOff := binary.LittleEndian.Uint32(raw[0x3C:0x40])
			if peOff+24 <= uint32(len(raw)) && string(raw[peOff:peOff+4]) == "PE\x00\x00" {
				// machine:0x8664=amd64、**0xAA64=arm64**(2026-10-02 起进矩阵;
				// 不认它的话 arm64 产物会被报成 windows/?,而真正的问题要到目标机上暴露)。
				mach := binary.LittleEndian.Uint16(raw[peOff+4 : peOff+6])
				switch mach {
				case 0x8664:
					return "windows", "amd64"
				case 0xAA64:
					return "windows", "arm64"
				}
			}
		}
		return "windows", "?"
	}
	if len(raw) >= 4 && raw[0] == 0x7f && raw[1] == 'E' && raw[2] == 'L' && raw[3] == 'F' {
		// ELF: e_machine(little-endian,offset 18),amd64=62,arm64=183
		if len(raw) >= 20 {
			mach := binary.LittleEndian.Uint16(raw[18:20])
			switch mach {
			case 62:
				return "linux", "amd64"
			case 183:
				return "linux", "arm64"
			}
		}
		return "linux", "?"
	}
	// Mach-O 64(little: CF FA ED FE / big: FE ED FA CF)
	if len(raw) >= 8 && ((raw[0] == 0xcf && raw[1] == 0xfa && raw[2] == 0xed && raw[3] == 0xfe) ||
		(raw[0] == 0xfe && raw[1] == 0xed && raw[2] == 0xfa && raw[3] == 0xcf)) {
		var cpu uint32
		if raw[0] == 0xfe { // 魔数 FE ED FA CF = big-endian
			cpu = binary.BigEndian.Uint32(raw[4:8])
		} else { // 魔数 CF FA ED FE = little-endian
			cpu = binary.LittleEndian.Uint32(raw[4:8])
		}
		switch cpu {
		case 0x01000007:
			return "darwin", "amd64"
		case 0x0100000c:
			return "darwin", "arm64"
		}
		return "darwin", "?"
	}
	return "?", "?"
}

// platformDirName 当前平台对应的 embed 子目录名。
func platformDirName() string {
	return extPluginDir[strings.LastIndex(extPluginDir, "/")+1:]
}

// TestExtPluginsMatchPlatform P4 回归:本平台 embed 产物解压后
// 头部标识的 OS/arch 必须等于构建平台;其他平台产物不得混入(交叉编译漂移护栏)。
func TestExtPluginsMatchPlatform(t *testing.T) {
	entries, err := fs.ReadDir(extPlugins, extPluginDir)
	if err != nil {
		t.Fatal(err)
	}
	var packed []fs.DirEntry
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ExtPluginExt) {
			packed = append(packed, e)
		}
	}
	// 期望集 = gen-extplugins.sh NAMES(darwin/linux/windows 各 4 个产物)。
	if len(packed) != 4 {
		t.Fatalf("本平台应恰好 4 个插件产物,got %d(%v)", len(packed), packed)
	}
	if platformDirName() != runtime.GOOS+"-"+runtime.GOARCH {
		t.Fatalf("embed 目录 %s 与构建平台 %s/%s 不符(build-tag 漂移)", platformDirName(), runtime.GOOS, runtime.GOARCH)
	}
	for _, e := range packed {
		f, err := extPlugins.Open(extPluginDir + "/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		dec, err := zstd.NewReader(f)
		if err != nil {
			f.Close()
			t.Fatal(err)
		}
		raw, err := io.ReadAll(dec.IOReadCloser())
		dec.Close()
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		osName, arch := binArch(raw)
		if osName != runtime.GOOS || arch != runtime.GOARCH {
			t.Fatalf("%s 产物架构 %s/%s != 构建平台 %s/%s(发行产物将不可执行)",
				e.Name(), osName, arch, runtime.GOOS, runtime.GOARCH)
		}
	}
}

// TestBinArchPEArchitectures PE machine 字段的判定(**合成头,不依赖本机平台**)。
//
// 为什么单列:TestExtPluginsMatchPlatform 是**按宿主平台**检查的(本机只查 darwin 那份
// embed),所以「PE arm64 认不认得出来」这条判定在 mac 上根本走不到 —— 而它恰恰是
// windows/arm64 进矩阵后最容易漏的一处(漏了的表现是:arm64 产物被报成 windows/?,
// 而真正的问题要到目标机上才暴露)。
func TestBinArchPEArchitectures(t *testing.T) {
	mkPE := func(machine uint16) []byte {
		// 布局必须**精确**:MZ 头 0x80 字节、e_lfanew=0x80、PE 头紧随其后 ——
		// 填充长度差一点就读不到 "PE\x00\x00"(合成头初版就栽在这里,三个用例全报 ?/?)。
		const peOff = 0x80
		raw := make([]byte, peOff)
		raw[0], raw[1] = 'M', 'Z'
		binary.LittleEndian.PutUint32(raw[0x3C:0x40], peOff)
		hdr := make([]byte, 0x18)
		hdr[0], hdr[1], hdr[2], hdr[3] = 'P', 'E', 0, 0
		binary.LittleEndian.PutUint16(hdr[4:6], machine)
		return append(raw, hdr...)
	}
	cases := []struct {
		machine  uint16
		wantOS   string
		wantArch string
		note     string
	}{
		{0x8664, "windows", "amd64", "x64"},
		{0xAA64, "windows", "arm64", "AArch64(2026-10-02 进矩阵)"},
		{0x01C4, "windows", "?", "arm32 不在矩阵内 ⇒ 报出来而不是猜"},
	}
	for _, c := range cases {
		os1, arch := binArch(mkPE(c.machine))
		if os1 != c.wantOS || arch != c.wantArch {
			t.Errorf("machine 0x%04X(%s):got %s/%s,want %s/%s", c.machine, c.note, os1, arch, c.wantOS, c.wantArch)
		}
	}
}
