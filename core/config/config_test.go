package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTreeApplyReplaceAndInsert(t *testing.T) {
	tree := NewTree()
	tree.Apply([]Entry{{ID: "a", Data: map[string]any{"v": 1}}, {ID: "b", Data: map[string]any{"v": 2}}})
	// 同 id 替换,新 id 插入
	tree.Apply([]Entry{{ID: "a", Data: map[string]any{"v": 99}}, {ID: "c", Data: map[string]any{"v": 3}}})
	if v, _ := tree.Get("a"); v.Data["v"] != 99 {
		t.Fatalf("同 id 应替换,got %v", v.Data)
	}
	got := tree.List()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("顺序应保持 a,b,c,got %v", got)
	}
}

func TestTreeEnabled(t *testing.T) {
	tree := NewTree()
	tree.Apply([]Entry{{ID: "on"}, {ID: "off", Enabled: boolPtr(false)}, {ID: "explicitOn", Enabled: boolPtr(true)}})
	if !tree.Enabled("on") || tree.Enabled("off") || !tree.Enabled("explicitOn") || tree.Enabled("missing") {
		t.Fatal("enabled 语义:缺省启用,显式 false 关闭,缺失不存在")
	}
}

func boolPtr(b bool) *bool { return &b }

func TestProfileBundlePatchMerge(t *testing.T) {
	dir := t.TempDir()
	// 三个 bundle 文件 + profile + patch
	bundles := map[string]string{
		"base":  "entries:\n  - id: plugin-a\n  - id: plugin-b\n",
		"extra": "entries:\n  - id: plugin-c\n",
	}
	for name, content := range bundles {
		if err := os.WriteFile(filepath.Join(dir, "bundle-"+name+".yaml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	patchP := filepath.Join(dir, "patch-example.yaml")
	os.WriteFile(patchP, []byte("entries:\n  - id: plugin-b\n    enabled: false\n  - id: plugin-d\n"), 0o644)

	profileP := filepath.Join(dir, "profile.yaml")
	os.WriteFile(profileP, []byte("name: dev\nbundles:\n  - base\n  - extra\npatches:\n  - patch-example.yaml\n"), 0o644)

	resolve := func(name string) ([]Entry, error) {
		b, err := ReadBundle(filepath.Join(dir, "bundle-"+name+".yaml"))
		if err != nil {
			return nil, err
		}
		return b.Entries, nil
	}

	tree, err := LoadProfile(profileP, resolve)
	if err != nil {
		t.Fatal(err)
	}
	// 合并顺序:base(a,b) → extra(c) → patch(替换 b 关闭,插入 d)
	if got := tree.List(); len(got) != 4 || got[0] != "plugin-a" || got[1] != "plugin-b" || got[2] != "plugin-c" || got[3] != "plugin-d" {
		t.Fatalf("合并顺序应为 a,b,c,d,got %v", got)
	}
	if tree.Enabled("plugin-b") {
		t.Fatal("patch 应能把 bundle 中的条目关闭")
	}
	if !tree.Enabled("plugin-a") || !tree.Enabled("plugin-c") || !tree.Enabled("plugin-d") {
		t.Fatal("未关闭条目应保持启用")
	}
}

func TestSaveAndRecoverBackup(t *testing.T) {
	dir := t.TempDir()
	tree := NewTree()
	tree.Apply([]Entry{{ID: "good-a", Data: map[string]any{"v": 1}}, {ID: "good-b"}})
	if err := SaveBackup(tree, dir, 3); err != nil {
		t.Fatal(err)
	}
	// 再存一份(覆盖 latest)
	tree2 := NewTree()
	tree2.Apply([]Entry{{ID: "good-a", Data: map[string]any{"v": 2}}})
	if err := SaveBackup(tree2, dir, 3); err != nil {
		t.Fatal(err)
	}
	recovered, err := LoadLatestBackup(dir)
	if err != nil {
		t.Fatal(err)
	}
	if recovered == nil {
		t.Fatal("应有备份")
	}
	if e, _ := recovered.Get("good-a"); e.Data["v"] != 2 {
		t.Fatalf("latest 应是最新配置,got %v", e.Data)
	}
	if len(recovered.List()) != 1 {
		t.Fatalf("备份内容应与 tree2 一致,got %v", recovered.List())
	}
}

func TestLoadLatestBackupAbsent(t *testing.T) {
	recovered, err := LoadLatestBackup(t.TempDir())
	if err != nil || recovered != nil {
		t.Fatalf("无备份时应返回 nil,nil,got %v %v", recovered, err)
	}
}

func TestDumpRoundTrip(t *testing.T) {
	tree := NewTree()
	tree.Apply([]Entry{{ID: "x", Data: map[string]any{"k": "v"}}})
	raw, err := tree.DumpYAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "id: x") {
		t.Fatalf("dump 应包含条目 x,got %s", raw)
	}
	// 重新解析并应用
	var p Patch
	if err := yaml.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	t2 := NewTree()
	t2.Apply(p.Entries)
	if !t2.Enabled("x") {
		t.Fatal("round-trip 后 x 应存在且启用")
	}
}
