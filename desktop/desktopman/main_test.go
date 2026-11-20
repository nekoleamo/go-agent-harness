package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// write 测试辅助。
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSignVerifyRoundTrip 发布工具的自检闭环:签名 → 校验通过;改一个字节 → 校验失败。
// 这条链错了,发出去的包在用户机器上装不上(而且 CI 里看不出来)。
// 反向验证:把 verify 的摘要比较去掉,第二条断言必红。
func TestSignVerifyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "update.key")
	write(t, keyPath, hex.EncodeToString(priv)+"\n")

	artifact := filepath.Join(dir, "app.zip")
	write(t, artifact, "payload-v1")
	manifestPath := filepath.Join(dir, "desktop-latest.json")

	if err := cmdSign([]string{
		"--key", keyPath, "--artifact", artifact, "--platform", "darwin-aarch64",
		"--version", "0.1.7", "--url", "https://example.com/app.zip", "--out", manifestPath,
	}); err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	if err := cmdVerify([]string{
		"--manifest", manifestPath, "--platform", "darwin-aarch64",
		"--artifact", artifact, "--pubkey", hex.EncodeToString(pub),
	}); err != nil {
		t.Fatalf("校验应通过: %v", err)
	}

	// 产物被改过(哪怕一个字节)⇒ 摘要不匹配。
	write(t, artifact, "payload-v2")
	if err := cmdVerify([]string{
		"--manifest", manifestPath, "--platform", "darwin-aarch64", "--artifact", artifact,
	}); err == nil {
		t.Fatal("内容变了必须校验失败")
	}
}

// TestSignRefusesMixedVersions 一份清单只能描述一个版本:混版本会让用户装上错的包。
func TestSignRefusesMixedVersions(t *testing.T) {
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "update.key")
	write(t, keyPath, hex.EncodeToString(priv)+"\n")
	art := filepath.Join(dir, "a.zip")
	write(t, art, "x")
	out := filepath.Join(dir, "m.json")

	base := []string{"--key", keyPath, "--artifact", art, "--platform", "darwin-aarch64", "--url", "u", "--out", out}
	if err := cmdSign(append(base, "--version", "0.1.7")); err != nil {
		t.Fatal(err)
	}
	// 同版本、不同平台 ⇒ 合并成功(这是正常用法)。
	if err := cmdSign(append(base, "--version", "0.1.7", "--platform", "windows-x86_64")); err != nil {
		t.Fatalf("同版本多平台应允许合并: %v", err)
	}
	// 不同版本 ⇒ 必须拒绝。
	if err := cmdSign(append(base, "--version", "0.1.8")); err == nil {
		t.Fatal("混版本必须拒绝")
	}
}

// TestHashFile 摘要口径必须与壳侧一致(sha256 over 整个文件)。
func TestHashFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	write(t, p, "abc")
	sum, size, err := hashFile(p)
	if err != nil || size != 3 {
		t.Fatalf("hashFile: sum=%x size=%d err=%v", sum, size, err)
	}
	want := sha256.Sum256([]byte("abc"))
	if hex.EncodeToString(sum) != hex.EncodeToString(want[:]) {
		t.Fatal("摘要不是 sha256(整文件)")
	}
}
