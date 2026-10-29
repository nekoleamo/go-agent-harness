//go:build !embed_sidecar

// 开发构建(不带 embed_sidecar tag):没有内嵌 sidecar,壳从磁盘找
// (`GAH_SIDECAR` / 与壳同目录 / binaries/gah-<triple>),便于边改 gah 边跑壳。
package main

var sidecarGz []byte
