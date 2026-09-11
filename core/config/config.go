// Package config 实现配置层:profile → bundle → patch 按序合并,对齐 cordis.patch.yml。
// 语义:
//   - profile 是具名组装:列出按序应用的 bundle(每个 bundle 是一组 entry)与 patch 文件;
//   - 每一条 patch 按 entry id 定位:同 id 替换整个 config,新 id 插入;
//   - 合并结果 = 空表 + bundle 按序应用 + profile.patch + home patch + 命令行 overlay;
//   - 任一 entry 可用 enabled: false 关闭(插拔开关的配置层表达)。
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Entry 是配置树中的一个条目:identity + 配置负载。
type Entry struct {
	ID      string `yaml:"id" json:"id"`
	Enabled *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Data 为该条目的完整配置负载(插件自解释,配置层不校验语义)。
	Data map[string]any `yaml:"data,omitempty" json:"data,omitempty"`
}

// Bundle 是配置项的分发格式:一组按序应用的 entry 列表(对齐 dsh bundle)。
type Bundle struct {
	Name    string  `yaml:"name"`
	Entries []Entry `yaml:"entries"`
}

// Profile 是具名组装:按序应用的 bundle 名 + 本 profile 的 patch 文件。
type Profile struct {
	Name    string   `yaml:"name"`
	Bundles []string `yaml:"bundles"`
	Patches []string `yaml:"patches"` // patch 文件路径(相对 profile 文件所在目录)
}

// Patch 文件:按 entry id 替换/插入。
type Patch struct {
	Entries []Entry `yaml:"entries"`
}

// Tree 是合并后的配置树:entryID → Entry,保持应用顺序。
type Tree struct {
	order []string
	byID  map[string]Entry
}

// Apply 按序应用一层 entry 列表:同 id 替换,新 id 追加。
func (t *Tree) Apply(entries []Entry) {
	for _, e := range entries {
		if _, ok := t.byID[e.ID]; !ok {
			t.order = append(t.order, e.ID)
		}
		t.byID[e.ID] = e
	}
}

// Get 取回条目。
func (t *Tree) Get(id string) (Entry, bool) {
	e, ok := t.byID[id]
	return e, ok
}

// Enabled 判断条目是否启用(默认启用;enabled=false 关闭)。
func (t *Tree) Enabled(id string) bool {
	e, ok := t.byID[id]
	if !ok {
		return false
	}
	return e.Enabled == nil || *e.Enabled
}

// List 返回全部条目(id 按应用顺序)。
func (t *Tree) List() []string {
	return append([]string(nil), t.order...)
}

// OrderedEntries 返回按应用顺序的条目切片(供 --dump-config 输出)。
func (t *Tree) OrderedEntries() []Entry {
	out := make([]Entry, 0, len(t.order))
	for _, id := range t.order {
		out = append(out, t.byID[id])
	}
	return out
}

// DumpYAML 序列化整棵树(与输入同构,可再次 apply)。
func (t *Tree) DumpYAML() ([]byte, error) {
	return yaml.Marshal(struct {
		Entries []Entry `yaml:"entries"`
	}{Entries: t.OrderedEntries()})
}

// NewTree 创建空配置树。
func NewTree() *Tree {
	return &Tree{byID: make(map[string]Entry)}
}

// LoadProfile 解析 profile 文件,并按序叠加其 bundle 与 patch(相对路径以 profile 目录解析)。
// bundles 参数提供 bundle 名 → 文件路径的解析器(由调用方注入,便于 embed 资源)。
type BundleResolver func(name string) ([]Entry, error)

func LoadProfile(profilePath string, resolve BundleResolver) (*Tree, error) {
	raw, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, fmt.Errorf("config: read profile %s: %w", profilePath, err)
	}
	var p Profile
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("config: parse profile %s: %w", profilePath, err)
	}
	dir := filepath.Dir(profilePath)

	t := NewTree()
	for _, b := range p.Bundles {
		if resolve == nil {
			return nil, errors.New("config: bundle resolver required when profile lists bundles")
		}
		entries, err := resolve(b)
		if err != nil {
			return nil, fmt.Errorf("config: bundle %q (from profile %s): %w", b, profilePath, err)
		}
		t.Apply(entries)
	}
	// profile 自身 patch(在 profile 文件所在目录解析相对路径)
	for _, pth := range p.Patches {
		abs := pth
		if !filepath.IsAbs(pth) {
			abs = filepath.Join(dir, pth)
		}
		entries, err := ReadPatch(abs)
		if err != nil {
			return nil, err
		}
		t.Apply(entries)
	}
	return t, nil
}

// ReadPatch 读取 patch 文件并返回其条目。
func ReadPatch(path string) ([]Entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read patch %s: %w", path, err)
	}
	var p Patch
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("config: parse patch %s: %w", path, err)
	}
	return p.Entries, nil
}

// BundlesOfProfile 只读 profile 声明的 bundle 列表(供 boot 装配器调度,避免破坏 LoadProfile 签名)。
func BundlesOfProfile(profilePath string) ([]string, error) {
	raw, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return p.Bundles, nil
}

// —— 配置自愈(对齐设计 M5.5:启动失败不直接退出,回滚最近正常备份重试一次) ——

// BackupDir 备份目录名(与生效配置相对)。
const BackupDir = "config-backups"

// SaveBackup 把当前生效配置树序列化到备份目录(保留最近 maxKeep 份)。
// 路径:同目录下 BackupDir/;文件名按时间戳,gah.yaml 恒为最新一份(启动时覆盖)。
func SaveBackup(t *Tree, configDir string, maxKeep int) error {
	raw, err := t.DumpYAML()
	if err != nil {
		return err
	}
	dir := filepath.Join(configDir, BackupDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	latest := filepath.Join(dir, "config.latest.yaml")
	if err := writeFileAtomic(latest, raw, 0o644); err != nil {
		return err
	}
	// 轮换保留
	snap := filepath.Join(dir, fmt.Sprintf("config.%s.yaml", time.Now().Format("20060102-150405")))
	if err := writeFileAtomic(snap, raw, 0o644); err != nil {
		return err
	}
	entries, _ := os.ReadDir(dir)
	var snaps []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == "config.latest.yaml" {
			continue
		}
		snaps = append(snaps, filepath.Join(dir, e.Name()))
	}
	for len(snaps) > maxKeep {
		os.Remove(snaps[0])
		snaps = snaps[1:]
	}
	return nil
}

// writeFileAtomic 同目录临时文件 + fsync + rename 原子落盘:备份文件被半截写坏会让
// 自愈路径(LoadLatestBackup)解析失败而彻底失去回滚能力,故不能直接覆写。
func writeFileAtomic(path string, raw []byte, perm os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		if name != "" {
			_ = os.Remove(name)
		}
	}()
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(name, perm); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	name = ""
	return nil
}

// LoadLatestBackup 读回最近一次备份(latest.yaml),缺省返回 nil(无备份可回滚)。
func LoadLatestBackup(configDir string) (*Tree, error) {
	p := filepath.Join(configDir, BackupDir, "config.latest.yaml")
	raw, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // 无备份(首次启动)
		}
		// 备份存在但不可读(权限/EIO):不能当“无备份”,否则自愈路径静默降级。
		return nil, fmt.Errorf("config: read backup %s: %w", p, err)
	}
	type treeShape struct {
		Entries []Entry `yaml:"entries"`
	}
	var shape treeShape
	if err := yaml.Unmarshal(raw, &shape); err != nil {
		return nil, err
	}
	t := NewTree()
	t.Apply(shape.Entries)
	return t, nil
}

// ReadBundle 读取 bundle 文件(独立使用的便捷入口)。
func ReadBundle(path string) (*Bundle, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read bundle %s: %w", path, err)
	}
	var b Bundle
	if err := yaml.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("config: parse bundle %s: %w", path, err)
	}
	return &b, nil
}
