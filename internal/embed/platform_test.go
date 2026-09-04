// P4 平台匹配:embed 的外部插件产物必须与当前构建平台一致(否则首启释放的
// 插件二进制无法在目标机执行——发行产物平台匹配回归护栏)。
package embed

import (
	"compress/gzip"
	"encoding/binary"
	"io"
	"io/fs"
	"runtime"
	"strings"
	"testing"
)

// binArch 读取可执行文件头部,识别平台与架构(ELF/Mach-O/PE 魔数)。
func binArch(raw []byte) (osName, arch string) {
	if len(raw) >= 2 && raw[0] == 'M' && raw[1] == 'Z' {
		// PE: DOS e_lfanew(offset 0x3C)→ PE 头 → machine(offset +4)
		if len(raw) >= 4+64+4+2 {
			peOff := binary.LittleEndian.Uint32(raw[0x3C:0x40])
			if peOff+24 <= uint32(len(raw)) && string(raw[peOff:peOff+4]) == "PE\x00\x00" {
				mach := binary.LittleEndian.Uint16(raw[peOff+4 : peOff+6])
				if mach == 0x8664 {
					return "windows", "amd64"
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

// TestExtPluginsMatchPlatform P4 回归:本平台 embed 产物(恰好 3 个)解压后
// 头部标识的 OS/arch 必须等于构建平台;其他平台产物不得混入(交叉编译漂移护栏)。
func TestExtPluginsMatchPlatform(t *testing.T) {
	entries, err := fs.ReadDir(extPlugins, extPluginDir)
	if err != nil {
		t.Fatal(err)
	}
	var gz []fs.DirEntry
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".gz") {
			gz = append(gz, e)
		}
	}
	if len(gz) != 3 {
		t.Fatalf("本平台应恰好 3 个插件产物,got %d(%v)", len(gz), gz)
	}
	if platformDirName() != runtime.GOOS+"-"+runtime.GOARCH {
		t.Fatalf("embed 目录 %s 与构建平台 %s/%s 不符(build-tag 漂移)", platformDirName(), runtime.GOOS, runtime.GOARCH)
	}
	for _, e := range gz {
		f, err := extPlugins.Open(extPluginDir + "/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		gzr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			t.Fatal(err)
		}
		raw, err := io.ReadAll(gzr)
		gzr.Close()
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
