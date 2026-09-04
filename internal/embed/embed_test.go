// guard:seed 与仓库 config/ 样板一致性(防止双重维护漂移)。
package embed

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsurePlugins 方案B首启释放:产物落 home/plugins/<name>/,幂等(不覆盖已有)。
func TestEnsurePlugins(t *testing.T) {
	home := t.TempDir()
	written, err := EnsurePlugins(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) == 0 {
		t.Fatal("应有随包插件释放")
	}
	// 产物可执行文件存在
	for _, w := range written {
		if fi, err := os.Stat(w); err != nil || fi.Mode()&0o111 == 0 {
			t.Fatalf("产物应存在且可执行: %s %v", w, err)
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
