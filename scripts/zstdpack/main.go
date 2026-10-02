// Command zstdpack 是外部插件产物的压缩器(2026-10-02 体积债路径 ③)。
//
// 为什么自己写而不调系统 `zstd`:`scripts/gen-extplugins.sh` 要在
// **五个平台的 CI runner** 上跑(ubuntu / macos / windows),而系统 zstd 并非处处
// 都有(macOS 与 Windows 镜像都不保证)。为一个压缩动作引入外部工具依赖,不如用
// 一个几十行的 Go 程序 —— 编码器纯 Go(`github.com/klauspost/compress/zstd`,
// 本仓已因其它间接依赖在树里),**结果只由输入决定**。
//
// 确定性:zstd 帧格式**不存 mtime / 文件名**,同样的输入 + 同样的级别 ⇒ 同样的字节
// (与旧实现 `gzip -9 -n` 的「重跑无 diff」是同一条纪律,见 gen-extplugins.sh 头注)。
//
// 压缩级别:用 `EncoderLevelFromZstd(19)` 对齐 zstd CLI 的 `-19`
// (klauspost 自己的 SpeedBestCompression 相当于 zstd 级别 11,省得少;实测 19 比 11 多省 ~4%)。
//
// 顺带产出 `SHA256SUMS`(未压缩内容的 sha256)。它的用途不是好看:宿主首启据此
// **在稳态下完全不解压** —— 磁盘上的插件产物哈希对得上就跳过解压(旧实现每次启动都要
// 把 4 件共 ~28 MiB 解压一遍再比对,实测 ~150ms)。有了清单,稳态只 hash 磁盘上的文件。
// 格式用 sha256sum 约定(「<hex>  <名字>」),Linux 上可直接 `sha256sum -c SHA256SUMS`。
//
// 用法:go run ./scripts/zstdpack <文件>...    # 就地生成 <文件>.zst、删原文件,并写同目录 SHA256SUMS
package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法:zstdpack <文件>...")
		os.Exit(2)
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(19)))
	if err != nil {
		fail("建编码器失败:%v", err)
	}
	defer enc.Close()
	var manifest []string
	dir := ""
	for _, in := range os.Args[1:] {
		raw, err := os.ReadFile(in)
		if err != nil {
			fail("读 %s 失败:%v", in, err)
		}
		if len(raw) == 0 {
			fail("%s 是空文件(压了也白压,还会让 embed 里出现一个 0 字节产物)", in)
		}
		out := in + ".zst"
		packed := enc.EncodeAll(raw, nil)
		if err := os.WriteFile(out, packed, 0o644); err != nil {
			fail("写 %s 失败:%v", out, err)
		}
		if err := os.Remove(in); err != nil {
			fail("删原文件 %s 失败:%v", in, err)
		}
		// 清单记**未压缩内容**的哈希:宿主比的是磁盘上解压后的那份文件。
		manifest = append(manifest, fmt.Sprintf("%x  %s", sha256.Sum256(raw), filepath.Base(in)))
		dir = filepath.Dir(in)
		fmt.Printf("  %s → %s (%.1f MiB → %.1f MiB)\n", in, out,
			float64(len(raw))/1048576, float64(len(packed))/1048576)
	}
	// 排序固定 ⇒ 清单字节确定(重跑无 diff)。
	sort.Strings(manifest)
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(strings.Join(manifest, "\n")+"\n"), 0o644); err != nil {
		fail("写 SHA256SUMS 失败:%v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "zstdpack: "+format+"\n", args...)
	os.Exit(1)
}
