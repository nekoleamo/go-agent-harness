// guard:seed 与仓库 config/ 样板一致性(防止双重维护漂移)。
package embed

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// assertNoTempLeftovers 断言目录里没留下写产物的临时文件(残留意味着 rename 没成)。
func assertNoTempLeftovers(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".gah-tmp-") {
			t.Fatalf("临时文件残留: %s", e.Name())
		}
	}
}

// TestEnsurePluginsUpgrade 自动升级:内容与 embed 不一致 → 覆盖;一致 → 跳过(幂等)。
// 覆盖旧插件二进制的能力缺失问题(如旧 tool-basic 缺 web_search),无需手动删除。
func TestEnsurePluginsUpgrade(t *testing.T) {
	home := t.TempDir()
	if _, err := EnsurePlugins(home); err != nil {
		t.Fatal(err)
	}
	// 写一个内容错误的旧产物(模拟旧版本二进制)
	dst := ""
	names, err := listNames(extPlugins, extPluginDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if strings.HasSuffix(n, ".gz") {
			dst = pluginDst(home, n)
			break
		}
	}
	if dst == "" {
		t.Fatal("无外部插件产物")
	}
	if err := os.WriteFile(dst, []byte("stale-plugin-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 记下覆盖前的文件身份:覆盖必须换 inode —— macOS 上旧 inode 会被 taskgated
	// 判「Code Signature Invalid」SIGKILL(详见 DESIGN R23)。os.SameFile 跨平台比较
	// Unix 的 dev+ino / Windows 的文件索引,不必引 syscall。
	oldFi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	// 二次 EnsurePlugins:内容不同 → 覆盖为 embed 产物
	upgraded, err := EnsurePlugins(home)
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, w := range upgraded {
		if w == dst {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("旧内容应被覆盖并写入: %v", upgraded)
	}
	raw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "stale-plugin-binary" {
		t.Fatal("旧内容应被覆盖")
	}
	if newFi, err := os.Stat(dst); err == nil && os.SameFile(oldFi, newFi) {
		t.Fatal("覆盖必须换文件身份(旧文件在 macOS 上可能被 taskgated 杀)")
	}
	assertNoTempLeftovers(t, filepath.Dir(dst))
	// 第三次:内容已一致 → 跳过
	repeat, err := EnsurePlugins(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeat) != 0 {
		t.Fatalf("内容一致后应幂等跳过: %v", repeat)
	}
}

// TestEnsurePlugins 方案B首启释放:产物落 home/plugins/<name>/,幂等(不覆盖已有/内容一致)。
func TestEnsurePlugins(t *testing.T) {
	home := t.TempDir()
	written, err := EnsurePlugins(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) == 0 {
		t.Fatal("应有随包插件释放")
	}
	// 产物可执行文件存在。可执行位是 POSIX 概念:Windows 无 Unix 权限位,
	// .exe 的 Mode() 恒不含 0o111,故只在非 Windows 校验(存在性两侧都校验)。
	for _, w := range written {
		fi, err := os.Stat(w)
		if err != nil {
			t.Fatalf("产物应存在: %s %v", w, err)
		}
		if runtime.GOOS != "windows" && fi.Mode()&0o111 == 0 {
			t.Fatalf("产物应可执行: %s mode=%v", w, fi.Mode())
		}
	}
	// 幂等:二次释放不写、不覆盖
	repeat, err := EnsurePlugins(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeat) != 0 {
		t.Fatalf("二次释放应为空: %v", repeat)
	}
}

// TestSeedVersionUpgrade 样板版本化升级:落盘 bundle 版本低于 seed → 备份后覆盖;
// 版本一致 → 不覆盖(用户编辑保留);无版本旧文件 → 升级(老用户自动补齐能力条目)。
func TestSeedVersionUpgrade(t *testing.T) {
	raw, err := Seed.ReadFile("seed/bundle-base.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cur := seedVersion(raw)
	if cur < 1 {
		t.Fatalf("seed 应声明 seed-version>=1, got %d", cur)
	}

	run := func(t *testing.T, old string, wantUpgraded bool) {
		t.Helper()
		home := t.TempDir()
		dst := filepath.Join(home, "config", "bundle-base.yaml")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte(old), 0o644); err != nil {
			t.Fatal(err)
		}
		written, err := EnsureSeed(home)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		found := func() bool {
			for _, w := range written {
				if w == dst {
					return true
				}
			}
			return false
		}
		if wantUpgraded {
			if string(got) != string(raw) {
				t.Fatalf("旧版应升级为 seed 新版:\n%s", got)
			}
			if !found() {
				t.Fatalf("升级应记录 dst 写入: %v", written)
			}
			// 备份存在且为旧内容
			entries, err := os.ReadDir(filepath.Join(home, "config"))
			if err != nil {
				t.Fatal(err)
			}
			var bak []string
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "bundle-base.yaml.bak-") {
					bak = append(bak, e.Name())
				}
			}
			if len(bak) == 0 {
				t.Fatal("升级应生成 .bak-* 备份")
			}
			bakRaw, _ := os.ReadFile(filepath.Join(home, "config", bak[0]))
			if string(bakRaw) != old {
				t.Fatal("备份内容应为旧样板")
			}
		} else {
			if string(got) != old {
				t.Fatal("版本一致时用户编辑不应被覆盖")
			}
			if found() {
				t.Fatalf("同版本不应写入 dst: %v", written)
			}
			// 其余缺失样板正常补写(bundle-tui 等)
			if len(written) == 0 {
				t.Fatal("缺失样板应正常补写")
			}
		}
	}

	// 无版本旧文件(最老用户)→ 升级
	t.Run("legacy-no-version-upgrades", func(t *testing.T) {
		run(t, "# 旧版样板\nname: base\nentries:\n", true)
	})
	// 显式低版本 → 升级
	t.Run("older-version-upgrades", func(t *testing.T) {
		run(t, "# seed-version: "+fmt.Sprint(cur-1)+"\nname: base\nentries:\n", true)
	})
	// 同版本 → 保留用户编辑
	t.Run("same-version-keeps-edits", func(t *testing.T) {
		run(t, "# seed-version: "+fmt.Sprint(cur)+"\nname: base\nentries:\n  - id: host-tools\n    data:\n      custom: true\n", false)
	})
}

func TestSeedMatchesRepoConfig(t *testing.T) {
	names, err := FileNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("seed 应为空")
	}
	repo := filepath.Join("..", "..", "config")
	for _, n := range names {
		seedRaw, err := Seed.ReadFile("seed/" + n)
		if err != nil {
			t.Fatal(err)
		}
		repoRaw, err := os.ReadFile(filepath.Join(repo, n))
		if err != nil {
			t.Fatalf("seed %s 在仓库 config/ 缺失: %v", n, err)
		}
		if strings.TrimSpace(string(seedRaw)) != strings.TrimSpace(string(repoRaw)) {
			t.Fatalf("seed/%s 与 config/%s 不一致(修改样板需同步两处)", n, n)
		}
	}
}

func TestEnsureSeedWritesOnce(t *testing.T) {
	home := t.TempDir()
	written, err := EnsureSeed(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) == 0 {
		t.Fatal("应释放样板")
	}
	// 二次调用:已存在,不覆盖
	written2, err := EnsureSeed(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(written2) != 0 {
		t.Fatalf("二次释放应 no-op,got %v", written2)
	}
	// 用户编辑不被覆盖
	p := filepath.Join(home, "config", "profile-tui.yaml")
	if err := os.WriteFile(p, []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureSeed(home); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != "custom" {
		t.Fatal("用户编辑的配置不应被 seed 覆盖")
	}
}
