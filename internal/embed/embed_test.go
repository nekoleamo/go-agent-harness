// guard:seed 与仓库 config/ 样板一致性(防止双重维护漂移)。
package embed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
