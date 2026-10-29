//go:build embed_sidecar

// 发行构建:sidecar 以 gzip 形式**内嵌**进壳二进制。
//
// 为什么内嵌而不是当第二个文件发:
//  1. wails updater 只换**单个**落盘目标(单文件 / 一个 zip 顶层条目)。壳与 sidecar
//     分两个文件时,Windows 上换掉壳就换不掉 sidecar ⇒ 新壳配旧 sidecar,正是旧壳花大力气
//     防的那类错配;
//  2. 发行包回到「一个可执行文件」,NSIS/zip/更新产物都少一层(用户装错版本的余地也小);
//  3. 版本天然锁死:壳里烧的是与它同批构建的 sidecar,不存在「壳 0.1.7 + sidecar 0.1.5」。
//
// 代价:壳从 11MB 变成 ~32MB(gzip 20.5MB + 壳 11.3MB),仍比旧 Tauri 方案(app 内两个
// 文件 ~35MB)小。解压只在首次/换版本时发生一次,产物落用户数据目录后被内容寻址复用。
//
// 生成:`bash scripts/gen-desktop-wails.sh --release` 会先写 embed/gah.bin.gz 再带本 tag 编译。
package main

import _ "embed"

//go:embed embed/gah.bin.gz
var sidecarGz []byte
