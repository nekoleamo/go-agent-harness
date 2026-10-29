// desktopman:桌面壳的更新清单工具(签名 + 合并)。
//
// 为什么自己写一个而不是用 `wails3 updater manifest`:那要装 wails3 CLI(一次 1.2GB),
// 而这次迁移的起点就是「不想再养一套大工具链」。这里只依赖标准库,和壳共用同一个 module。
//
// 用法:
//
//	# 1) 生成密钥对(私钥留本机/CI secret,公钥注入壳)
//	go run ./desktop/wails/cmd/desktopman keygen --out ~/.gah-desktop/update.key
//
//	# 2) 给一个产物签名并写进清单(多平台多次执行,自动合并)
//	go run ./desktop/wails/cmd/desktopman sign \
//	    --key ~/.gah-desktop/update.key --out dist-desktop/desktop-latest.json \
//	    --artifact dist-desktop/mac/gah.zip --platform darwin-aarch64 \
//	    --version 0.1.7 --url https://example.com/gah.zip --notes "修了什么"
//
//	# 3) 自检:清单里的摘要与签名是否和产物对得上(上传前必跑)
//	go run ./desktop/wails/cmd/desktopman verify --manifest dist-desktop/desktop-latest.json \
//	    --artifact mac=dist-desktop/mac/gah.zip --platform darwin-aarch64
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// manifest 与壳侧 internal 结构保持一致(字段名即契约)。
type manifest struct {
	Version   string                   `json:"version"`
	Notes     string                   `json:"notes,omitempty"`
	PubDate   string                   `json:"pub_date,omitempty"`
	Artifacts map[string]artifactEntry `json:"artifacts"`
}

type artifactEntry struct {
	URL    string `json:"url"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256"`
	Sig    string `json:"sig"`
	File   string `json:"filename,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = cmdKeygen(os.Args[2:])
	case "sign":
		err = cmdSign(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "pack":
		err = cmdPack(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		err = fmt.Errorf("未知子命令 %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "desktopman: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `用法:
  desktopman keygen --out <私钥路径>
  desktopman sign   --key <私钥> --artifact <产物> --platform <清单键> --version <版本> --url <下载地址> [--out 清单] [--notes 说明]
  desktopman verify --manifest <清单> --platform <清单键> --artifact <产物>
  desktopman pack   --in <文件> --entry <顶层条目名> --out <zip>

清单键:darwin-aarch64 / darwin-x86_64 / windows-x86_64 / windows-aarch64
`)
}

// cmdKeygen 生成 ed25519 密钥对:私钥写文件(0600),公钥打印到 stdout(hex,注入壳用)。
func cmdKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := fs.String("out", "", "私钥输出路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("缺 --out")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	// 私钥存 hex(seed 32 字节即可推出完整私钥,但这里直接存 64 字节,读写都简单)。
	if err := os.WriteFile(*out, []byte(hex.EncodeToString(priv)+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Println(hex.EncodeToString(pub))
	fmt.Fprintf(os.Stderr, "私钥已写入 %s(勿入库);公钥见标准输出,用 -ldflags \"-X main.updatePubKeyHex=<公钥>\" 注入壳\n", *out)
	return nil
}

// cmdSign 计算摘要 + 签名,写进(或合并进)清单。
func cmdSign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	keyPath := fs.String("key", "", "私钥文件")
	artifact := fs.String("artifact", "", "产物路径(.zip / 单文件)")
	platform := fs.String("platform", "", "清单键,如 darwin-aarch64")
	version := fs.String("version", "", "版本号(不带 v)")
	url := fs.String("url", "", "下载地址")
	out := fs.String("out", "desktop-latest.json", "清单输出路径")
	notes := fs.String("notes", "", "更新说明")
	pubDate := fs.String("pub-date", "", "发布时间(RFC3339,缺省不改)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for name, v := range map[string]string{"key": *keyPath, "artifact": *artifact, "platform": *platform, "version": *version, "url": *url} {
		if v == "" {
			return fmt.Errorf("缺 --%s", name)
		}
	}
	priv, err := readKey(*keyPath)
	if err != nil {
		return err
	}
	sum, size, err := hashFile(*artifact)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(priv, sum)

	m := manifest{Artifacts: map[string]artifactEntry{}}
	if b, err := os.ReadFile(*out); err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("已有清单解析失败(不覆盖): %w", err)
		}
		if m.Artifacts == nil {
			m.Artifacts = map[string]artifactEntry{}
		}
	}
	// 版本不一致时**拒绝合并**:一份清单只能描述一个版本,混版本会让用户装上错的包。
	if m.Version != "" && m.Version != *version {
		return fmt.Errorf("清单里已有版本 %s,与本次 %s 不一致(要么换 --out,要么先清理)", m.Version, *version)
	}
	m.Version = *version
	if *notes != "" {
		m.Notes = *notes
	}
	if *pubDate != "" {
		m.PubDate = *pubDate
	}
	m.Artifacts[*platform] = artifactEntry{
		URL:    *url,
		Size:   size,
		SHA256: hex.EncodeToString(sum),
		Sig:    base64.StdEncoding.EncodeToString(sig),
		File:   filepath.Base(*artifact),
	}
	if err := writeManifest(*out, m); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "已写入 %s: %s %s(%d 字节)\n", *out, *platform, *version, size)
	return nil
}

// cmdVerify 上传前自检:清单里的摘要与签名必须对得上产物。
func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	mf := fs.String("manifest", "", "清单路径")
	platform := fs.String("platform", "", "清单键")
	artifact := fs.String("artifact", "", "产物路径")
	pub := fs.String("pubkey", "", "公钥 hex(缺省则只校验摘要)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	b, err := os.ReadFile(*mf)
	if err != nil {
		return err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	ent, ok := m.Artifacts[*platform]
	if !ok {
		return fmt.Errorf("清单里没有 %s", *platform)
	}
	sum, size, err := hashFile(*artifact)
	if err != nil {
		return err
	}
	if hex.EncodeToString(sum) != ent.SHA256 {
		return fmt.Errorf("摘要不一致: 清单 %s vs 产物 %s", ent.SHA256, hex.EncodeToString(sum))
	}
	if ent.Size != 0 && ent.Size != size {
		return fmt.Errorf("大小不一致: 清单 %d vs 产物 %d", ent.Size, size)
	}
	if *pub != "" {
		pk, err := hex.DecodeString(*pub)
		if err != nil || len(pk) != ed25519.PublicKeySize {
			return fmt.Errorf("公钥非法")
		}
		sig, err := base64.StdEncoding.DecodeString(ent.Sig)
		if err != nil {
			return fmt.Errorf("签名不是合法 base64: %w", err)
		}
		if !ed25519.Verify(pk, sum, sig) {
			return fmt.Errorf("签名校验失败")
		}
		fmt.Fprintln(os.Stderr, "签名 OK")
	}
	fmt.Fprintf(os.Stderr, "清单 OK: %s %s(version=%s)\n", *platform, m.Version, m.Version)
	return nil
}

// readKey 读私钥(hex,64 字节或 32 字节 seed)。
func readKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, fmt.Errorf("私钥不是合法 hex: %w", err)
	}
	switch len(raw) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	}
	return nil, fmt.Errorf("私钥长度非法(%d 字节)", len(raw))
}

// hashFile 计算 sha256 与文件大小。
func hashFile(path string) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return nil, 0, err
	}
	return h.Sum(nil), n, nil
}

// writeManifest 稳定输出(键排序),便于 CI 逐字节比对与 git diff。
func writeManifest(path string, m manifest) error {
	keys := make([]string, 0, len(m.Artifacts))
	for k := range m.Artifacts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := map[string]artifactEntry{}
	for _, k := range keys {
		ordered[k] = m.Artifacts[k]
	}
	out := struct {
		Version   string                   `json:"version"`
		Notes     string                   `json:"notes,omitempty"`
		PubDate   string                   `json:"pub_date,omitempty"`
		Artifacts map[string]artifactEntry `json:"artifacts"`
	}{m.Version, m.Notes, m.PubDate, ordered}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
