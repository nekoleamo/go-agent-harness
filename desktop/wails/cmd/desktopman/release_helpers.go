// release_helpers:verify-release 用到的小工具(与 sign/verify 共用口径)。
package main

import (
	"archive/zip"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
)

// readManifest 读清单(复用 sign/verify 的结构体,只此一份定义)。
func readManifest(p string) (manifest, error) {
	var m manifest
	b, err := os.ReadFile(p)
	if err != nil {
		return m, fmt.Errorf("读清单失败: %w", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("清单解析失败: %w", err)
	}
	if m.Artifacts == nil {
		m.Artifacts = map[string]artifactEntry{}
	}
	return m, nil
}

// entURLBase 取 URL 的最后一段(即产物文件名)。
func entURLBase(u string) string {
	if u == "" {
		return ""
	}
	return path.Base(u)
}

// locateArtifact 在产物目录里找清单指向的那个文件(平台子目录 + 根目录都找)。
func locateArtifact(dir, plat, name string) string {
	cands := []string{
		filepath.Join(dir, plat, name),
		filepath.Join(dir, name),
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// verifyDetached ed25519 验签(sign 用的是「对 sha256 摘要签名」,这里是同一口径)。
func verifyDetached(pub, digest, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("公钥长度非法")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), digest, sig) {
		return fmt.Errorf("验签失败(发布包与内置公钥不匹配)")
	}
	return nil
}

// checkSingleTopLevel zip 必须恰好一个顶层条目。
// 这不是洁癖:wails updater 只换**单个**落盘目标,多于一个顶层条目时无法确定换哪个,直接拒绝。
func checkSingleTopLevel(p string) error {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return fmt.Errorf("zip 打不开: %w", err)
	}
	defer func() { _ = zr.Close() }()
	tops := map[string]bool{}
	for _, f := range zr.File {
		name := f.Name
		for i := 0; i < len(name); i++ {
			if name[i] == '/' {
				name = name[:i]
				break
			}
		}
		if name != "" {
			tops[name] = true
		}
	}
	if len(tops) != 1 {
		keys := make([]string, 0, len(tops))
		for k := range tops {
			keys = append(keys, k)
		}
		return fmt.Errorf("zip 顶层条目必须恰好 1 个,实际 %v(updater 会拒绝)", keys)
	}
	return nil
}
