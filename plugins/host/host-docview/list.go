// 工作台目录列举(D1):有界深度/条目数、默认跳过重型目录、路径一律以调用方视角构造。
package hostdocview

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// listLimits 目录列举封顶。
const (
	listMaxEntries  = 2000 // 总条目
	listMaxPerDir   = 500  // 单目录条目
	listMaxDepth    = 4
	listWarnPerRoot = 8
)

// treeSkipDirs 默认不展开的重型/元数据目录(跳过并显式告知)。
var treeSkipDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true, "__pycache__": true,
	"dist": true, "build": true, ".next": true, ".nuxt": true, "coverage": true, "target": true,
	".idea": true, ".cache": true, ".gradle": true, ".terraform": true, "vendor": true,
}

// List 有界目录列举(文件树)。depth ≤ 0 → 2;> 4 → 4。
func (s *Service) List(_ context.Context, req sdk.DocRequest, depth int) (*sdk.DocTree, error) {
	if strings.TrimSpace(req.Path) == "" {
		req.Path = "." // 缺省 = 当前工作区根(模型 doc_list 无参调用)
	}
	abs, err := s.res.ResolveDir(req.Path, req.Strict)
	if err != nil {
		return nil, err
	}
	if depth <= 0 {
		depth = 2
	}
	if depth > listMaxDepth {
		depth = listMaxDepth
	}
	dir := abs
	if fi, serr := os.Stat(abs); serr == nil && !fi.IsDir() {
		dir = filepath.Dir(abs)
	}
	tree := &sdk.DocTree{Path: displayPath(req, dir), Name: filepath.Base(dir)}
	if depth == 0 {
		return tree, nil
	}
	baseLogical := strings.TrimSpace(req.Path)
	skipped := map[string]bool{}
	total := 0
	var walk func(d string, level int)
	walk = func(d string, level int) {
		ents, err := os.ReadDir(d)
		if err != nil {
			tree.Warnings = append(tree.Warnings, fmt.Sprintf("无法读取目录 %s: %v", filepath.Base(d), err))
			return
		}
		sort.Slice(ents, func(i, j int) bool {
			if ents[i].IsDir() != ents[j].IsDir() {
				return ents[i].IsDir()
			}
			return ents[i].Name() < ents[j].Name()
		})
		shown := 0
		for _, e := range ents {
			if total >= listMaxEntries || shown >= listMaxPerDir {
				if !containsMarker(tree.Truncated, "entries") {
					tree.Truncated = append(tree.Truncated, fmt.Sprintf("entries:%d", total))
					tree.Warnings = append(tree.Warnings, fmt.Sprintf("目录条目超出预算(上限 %d),已截断", listMaxEntries))
				}
				return
			}
			if e.IsDir() && treeSkipDirs[e.Name()] {
				skipped[e.Name()] = true
				continue
			}
			child := filepath.Join(d, e.Name())
			ent := s.docEntry(child, e, baseLogical, dir, req.Strict)
			tree.Entries = append(tree.Entries, ent)
			total++
			shown++
			if e.IsDir() && level+1 < depth {
				walk(child, level+1)
			}
		}
	}
	walk(dir, 0)
	if len(skipped) > 0 {
		names := make([]string, 0, len(skipped))
		for n := range skipped {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) > listWarnPerRoot {
			names = names[:listWarnPerRoot]
		}
		tree.Warnings = append(tree.Warnings, "已跳过重型/元数据目录(不展开):"+strings.Join(names, " "))
	}
	return tree, nil
}

// docEntry 构造一个条目(逻辑路径以调用方视角拼接,不回传宿主绝对路径)。
func (s *Service) docEntry(child string, e os.DirEntry, baseLogical, root string, strict bool) sdk.DocEntry {
	// 逻辑路径:优先按"调用方请求路径 + 相对本树根"拼接;请求为空时退化为相对名
	rel, err := filepath.Rel(root, child)
	if err != nil {
		rel = e.Name()
	}
	logical := path.Join(strings.TrimSuffix(strings.ReplaceAll(baseLogical, "\\", "/"), "/"), filepath.ToSlash(rel))
	if strings.TrimSpace(baseLogical) == "" {
		logical = filepath.ToSlash(rel)
	}
	ent := sdk.DocEntry{Name: e.Name(), Path: logical, Dir: e.IsDir()}
	if e.IsDir() {
		return ent
	}
	if fi, err := e.Info(); err == nil {
		ent.Size = fi.Size()
		ent.ModTime = fi.ModTime()
	}
	if f, ok := formatByExt(e.Name()); ok {
		ent.Format = f
	}
	if _, ok := s.extractors[ent.Format]; ok {
		ent.Previewable = true
	} else if ent.Format == "" {
		// 无扩展名:内容兜底(text/binary)也算可预览
		ent.Previewable = true
	}
	_ = strict
	return ent
}

func containsMarker(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
