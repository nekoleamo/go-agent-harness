// verify-release / rewrite-url:新链的发布校验与换源(替代旧 Tauri 链的
// scripts/verify-release.mjs + publish-desktop.sh rewrite-url)。
//
// 为什么放在这个小工具里而不是再写一个 node 脚本:新链全是 Go(壳 + 清单工具),
// 发布时不希望再引一个 node 运行时;而且这两个动作的判据(摘要/签名/顶层条目)
// 与 `sign`/`verify` 同源,放一起才不会再出现"两边算法不一致"。
package main

import (
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// cmdVerifyRelease 发布前/后的总校验:
//  1. 清单可解析、版本非空、至少一个平台;
//  2. 每个平台的产物**本地存在**,大小与 sha256 对得上;
//  3. 给了 --pubkey 就逐个验签(发布包里应始终给);
//  4. zip 产物必须是**唯一顶层条目**(updater 的硬要求,错了用户装不上)。
//
// 失败即非零退出:它是发布门,不是报告工具。
func cmdVerifyRelease(args []string) error {
	fs := flag.NewFlagSet("verify-release", flag.ContinueOnError)
	dir := fs.String("dir", "dist-desktop-wails", "产物目录(含 desktop-latest.json 与各平台子目录)")
	pub := fs.String("pubkey", "", "验签公钥 hex(强烈建议给;不给只校摘要)")
	expect := fs.String("expect", "", "期望版本(缺省取清单里的 version)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	manifestPath := filepath.Join(*dir, "desktop-latest.json")
	m, err := readManifest(manifestPath)
	if err != nil {
		return err
	}
	if m.Version == "" {
		return fmt.Errorf("%s 缺 version", manifestPath)
	}
	if *expect != "" && m.Version != *expect {
		return fmt.Errorf("清单版本 %s 与期望 %s 不一致", m.Version, *expect)
	}
	if len(m.Artifacts) == 0 {
		return fmt.Errorf("%s 没有任何平台产物", manifestPath)
	}

	var pubKey []byte
	if *pub != "" {
		pubKey, err = hex.DecodeString(*pub)
		if err != nil || len(pubKey) != 32 {
			return fmt.Errorf("--pubkey 不是 32 字节 hex")
		}
	}

	plats := make([]string, 0, len(m.Artifacts))
	for k := range m.Artifacts {
		plats = append(plats, k)
	}
	sort.Strings(plats)

	for _, plat := range plats {
		ent := m.Artifacts[plat]
		art := entURLBase(ent.URL)
		if art == "" {
			return fmt.Errorf("%s: 清单里的 url 为空", plat)
		}
		local := locateArtifact(*dir, plat, art)
		if local == "" {
			return fmt.Errorf("%s: 本地找不到产物 %s(下载地址里是这个名字)", plat, art)
		}
		sum, size, err := hashFile(local)
		if err != nil {
			return err
		}
		if hex.EncodeToString(sum) != ent.SHA256 {
			return fmt.Errorf("%s: sha256 不一致(清单 %s / 产物 %s)", plat, ent.SHA256, hex.EncodeToString(sum))
		}
		if ent.Size != 0 && ent.Size != size {
			return fmt.Errorf("%s: 大小不一致(清单 %d / 产物 %d)", plat, ent.Size, size)
		}
		if pubKey != nil {
			sig, err := base64.StdEncoding.DecodeString(ent.Sig)
			if err != nil {
				return fmt.Errorf("%s: 签名不是合法 base64: %w", plat, err)
			}
			if err := verifyDetached(pubKey, sum, sig); err != nil {
				return fmt.Errorf("%s: %w", plat, err)
			}
		}
		if filepath.Ext(local) == ".zip" {
			if err := checkSingleTopLevel(local); err != nil {
				return fmt.Errorf("%s: %w", plat, err)
			}
		}
		fmt.Printf("  ✓ %-18s %-40s %d 字节\n", plat, filepath.Base(local), size)
	}
	fmt.Printf("verify-release OK: version=%s platforms=%v\n", m.Version, plats)
	return nil
}

// cmdRewriteURL 把清单里的下载地址整体换到镜像前缀(换源不降低安全性:
// 签名只对**文件内容**验签,URL 不参与)。
func cmdRewriteURL(args []string) error {
	fs := flag.NewFlagSet("rewrite-url", flag.ContinueOnError)
	manifestPath := fs.String("manifest", "", "清单路径")
	base := fs.String("base", "", "新的下载前缀;传 none 表示还原成清单里的原始文件名(直链)")
	orig := fs.String("orig", "", "原始前缀(还原时用,例如 https://github.com/.../releases/latest/download)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *manifestPath == "" || *base == "" {
		return fmt.Errorf("缺 --manifest 或 --base")
	}
	m, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	for plat, ent := range m.Artifacts {
		name := entURLBase(ent.URL)
		if *base == "none" {
			if *orig == "" {
				return fmt.Errorf("还原直链需要 --orig")
			}
			ent.URL = trimSlash(*orig) + "/" + name
		} else {
			ent.URL = trimSlash(*base) + "/" + name
		}
		m.Artifacts[plat] = ent
	}
	if err := writeManifest(*manifestPath, m); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "已改写 %s 的 %d 个下载地址\n", *manifestPath, len(m.Artifacts))
	return nil
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
