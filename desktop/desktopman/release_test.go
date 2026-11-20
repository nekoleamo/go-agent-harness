package main

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeDist 造一个最小发行目录:产物 + 已签名清单。
func makeDist(t *testing.T, plat, version string, artifact []byte, zipEntries []string) (dir string, pub ed25519.PublicKey) {
	t.Helper()
	dir = t.TempDir()
	pubK, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	name := "gah-" + version + "-" + plat + ".zip"
	art := filepath.Join(dir, plat, name)
	if err := os.MkdirAll(filepath.Dir(art), 0o755); err != nil {
		t.Fatal(err)
	}
	if len(zipEntries) > 0 {
		f, err := os.Create(art)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		for _, e := range zipEntries {
			w, err := zw.Create(e)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(artifact)
		}
		_ = zw.Close()
		_ = f.Close()
	} else if err := os.WriteFile(art, artifact, 0o644); err != nil {
		t.Fatal(err)
	}
	sum, size, err := hashFile(art)
	if err != nil {
		t.Fatal(err)
	}
	m := manifest{Version: version, Artifacts: map[string]artifactEntry{
		plat: {
			URL:    "https://example.com/" + name,
			Size:   size,
			SHA256: hex.EncodeToString(sum),
			Sig:    base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sum)),
		},
	}}
	if err := writeManifest(filepath.Join(dir, "desktop-latest.json"), m); err != nil {
		t.Fatal(err)
	}
	return dir, pubK
}

// TestVerifyReleaseOK 正常发行目录应通过(含验签与 zip 顶层条目检查)。
func TestVerifyReleaseOK(t *testing.T) {
	dir, pub := makeDist(t, "darwin-aarch64", "0.1.7", []byte("payload"), []string{"gah.app/"})
	err := cmdVerifyRelease([]string{"--dir", dir, "--pubkey", hex.EncodeToString(pub), "--expect", "0.1.7"})
	if err != nil {
		t.Fatalf("应通过: %v", err)
	}
}

// TestVerifyReleaseCatchesBadDigest 产物被改过 ⇒ 必须拒绝。
//
// **刻意不传 --pubkey**:传了的话验签也会拦住它,这条用例就变成「恒绿」——
// 无论摘要检查在不在都通过(这正是本仓库反复强调的假用例形态)。
// 不传 pubkey 时只有摘要这一道闸,去掉它用例必红。
func TestVerifyReleaseCatchesBadDigest(t *testing.T) {
	// 产物必须是一个**真的 zip**(否则先被「zip 打不开」拦住,摘要那闸测不到)。
	dir, _ := makeDist(t, "darwin-aarch64", "0.1.7", []byte("payload"), []string{"gah.app/"})
	art := filepath.Join(dir, "darwin-aarch64", "gah-0.1.7-darwin-aarch64.zip")
	b, err := os.ReadFile(art)
	if err != nil {
		t.Fatal(err)
	}
	// **同长度**翻转一个字节:改长度会被「大小不一致」先拦住,
	// 那样这条用例又变成测不到摘要的恒绿。
	b[len(b)/2] ^= 0xff
	if err := os.WriteFile(art, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdVerifyRelease([]string{"--dir", dir}); err == nil {
		t.Fatal("摘要不一致必须拒绝")
	}
}

// TestVerifyReleaseCatchesBadSignature 清单里的签名与产物不匹配 ⇒ 必须拒绝
// (这条不传 pubkey 是拦不住的,所以它专门覆盖签名那一闸)。
func TestVerifyReleaseCatchesBadSignature(t *testing.T) {
	dir, pub := makeDist(t, "darwin-aarch64", "0.1.7", []byte("payload"), nil)
	mf := filepath.Join(dir, "desktop-latest.json")
	m, err := readManifest(mf)
	if err != nil {
		t.Fatal(err)
	}
	ent := m.Artifacts["darwin-aarch64"]
	sig, _ := base64.StdEncoding.DecodeString(ent.Sig)
	sig[0] ^= 0xff // 篡改一个字节
	ent.Sig = base64.StdEncoding.EncodeToString(sig)
	m.Artifacts["darwin-aarch64"] = ent
	if err := writeManifest(mf, m); err != nil {
		t.Fatal(err)
	}
	if err := cmdVerifyRelease([]string{"--dir", dir, "--pubkey", hex.EncodeToString(pub)}); err == nil {
		t.Fatal("签名不匹配必须拒绝")
	}
}

// TestVerifyReleaseCatchesMultiTopLevelZip 顶层条目 >1 ⇒ 拒绝(updater 会拒这种包)。
func TestVerifyReleaseCatchesMultiTopLevelZip(t *testing.T) {
	dir, pub := makeDist(t, "windows-x86_64", "0.1.7", []byte("payload"), []string{"a.exe", "b.exe"})
	err := cmdVerifyRelease([]string{"--dir", dir, "--pubkey", hex.EncodeToString(pub)})
	if err == nil {
		t.Fatal("多顶层条目必须拒绝")
	}
	if !contains(err.Error(), "顶层条目") {
		t.Fatalf("错误应说明原因: %v", err)
	}
}

// TestRewriteURL 换源只改 URL,不动摘要/签名(签名只对文件内容)。
func TestRewriteURL(t *testing.T) {
	dir, _ := makeDist(t, "darwin-aarch64", "0.1.7", []byte("payload"), nil)
	mf := filepath.Join(dir, "desktop-latest.json")
	if err := cmdRewriteURL([]string{"--manifest", mf, "--base", "https://gh-proxy.com/"}); err != nil {
		t.Fatal(err)
	}
	m, err := readManifest(mf)
	if err != nil {
		t.Fatal(err)
	}
	ent := m.Artifacts["darwin-aarch64"]
	want := "https://gh-proxy.com/gah-0.1.7-darwin-aarch64.zip"
	if ent.URL != want {
		t.Fatalf("换源后 URL = %q, want %q", ent.URL, want)
	}
	if ent.SHA256 == "" || ent.Sig == "" {
		t.Fatal("换源不该动摘要/签名")
	}
	// 还原直链
	if err := cmdRewriteURL([]string{"--manifest", mf, "--base", "none", "--orig", "https://github.com/o/r/releases/latest/download"}); err != nil {
		t.Fatal(err)
	}
	m2, _ := readManifest(mf)
	if got := m2.Artifacts["darwin-aarch64"].URL; got != "https://github.com/o/r/releases/latest/download/gah-0.1.7-darwin-aarch64.zip" {
		t.Fatalf("还原后 = %q", got)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
