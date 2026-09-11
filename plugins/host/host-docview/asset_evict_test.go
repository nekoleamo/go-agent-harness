// 资产表窗口淘汰(⑦)测试:资产按「最近预览文档窗口」整体淘汰,并与缓存视图联动失效。
package hostdocview

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestAssetEvictionByDocWindow 窗口(最近 maxAssetDocs 篇)外的文档资产整体淘汰,
// 窗口内资产保留;同一文档重复预览只上浮不新增窗口项。
func TestAssetEvictionByDocWindow(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	docs := make([]string, 0, maxAssetDocs+3)
	for i := 0; i < maxAssetDocs+3; i++ {
		doc := filepath.Join(dir, fmt.Sprintf("doc%d.md", i))
		docs = append(docs, doc)
		s.RegisterFileAsset(doc, filepath.Join(dir, fmt.Sprintf("img%d.png", i)), "image/png")
		s.notePreview(doc)
	}
	// 窗口 = 最后 maxAssetDocs 篇;更早的三篇资产应已淘汰
	if got := len(s.assets); got != maxAssetDocs {
		t.Fatalf("窗口内应只留 %d 篇的资产,实际 %d", maxAssetDocs, got)
	}
	for i := 0; i < 3; i++ {
		for id, ref := range s.assets {
			if ref.doc == docs[i] {
				t.Fatalf("窗口外文档的资产未淘汰: %s", id)
			}
		}
	}
	for i := 3; i < len(docs); i++ {
		found := false
		for _, ref := range s.assets {
			if ref.doc == docs[i] {
				found = true
			}
		}
		if !found {
			t.Fatalf("窗口内文档 %d 的资产不应被淘汰", i)
		}
	}
	if len(s.previewDocs) != maxAssetDocs || s.previewDocs[0] != docs[len(docs)-1] {
		t.Fatalf("窗口顺序错误: %v", s.previewDocs)
	}
	// 重复预览(同一文档)= 去重上浮,不额外淘汰
	before := len(s.assets)
	s.notePreview(docs[len(docs)-1])
	if len(s.assets) != before || len(s.previewDocs) != maxAssetDocs {
		t.Fatalf("重复预览不应改变窗口: entries=%d docs=%d", len(s.assets), len(s.previewDocs))
	}
}

// TestAssetEvictionHardCap 硬上限:单篇文档资产超上限时,从最旧文档起继续淘汰,
// 但**永不动最新一篇**(用户正在看的那篇必须完整,否则预览内图片全 404)。
func TestAssetEvictionHardCap(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	oldDoc := filepath.Join(dir, "old.md")
	for i := 0; i < maxAssetEntries+10; i++ {
		s.RegisterFileAsset(oldDoc, filepath.Join(dir, fmt.Sprintf("o%d.png", i)), "image/png")
	}
	s.notePreview(oldDoc)
	if got := len(s.assets); got <= maxAssetEntries {
		t.Fatalf("单篇超量资产应保留(不部分淘汰当前篇),实际 %d", got)
	}
	newDoc := filepath.Join(dir, "new.md")
	s.RegisterFileAsset(newDoc, filepath.Join(dir, "n0.png"), "image/png")
	s.notePreview(newDoc)
	if len(s.assets) != 1 {
		t.Fatalf("硬上限应把超量旧文档整篇淘汰,实际 %d", len(s.assets))
	}
	if len(s.previewDocs) != 1 || s.previewDocs[0] != newDoc {
		t.Fatalf("最新一篇应保留在窗口: %v", s.previewDocs)
	}
}

// TestAssetEvictionInvalidatesCache 资产淘汰同步失效该文档的缓存视图
// (否则缓存视图会引用已淘汰的资产 ID → 前端取回 404)。
func TestAssetEvictionInvalidatesCache(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	doc := filepath.Join(dir, "a.md")
	s.notePreview(doc) // 进入窗口(与真实路径一致:Preview 先 notePreview 再抽缓存)
	key := cacheKey{path: doc, size: 1, mtime: 1, params: ""}
	s.cache.put(key, &sdk.DocView{Path: doc, Name: "a.md"})
	if _, ok := s.cache.get(key); !ok {
		t.Fatal("前置:缓存应命中")
	}
	// 让 doc 掉出窗口
	for i := 0; i < maxAssetDocs+1; i++ {
		s.notePreview(filepath.Join(dir, fmt.Sprintf("other%d.md", i)))
	}
	if _, ok := s.cache.get(key); ok {
		t.Fatal("被淘汰文档的缓存视图应同步失效")
	}
	// 未被淘汰的文档缓存不受影响
	keep := filepath.Join(dir, "keep.md")
	key2 := cacheKey{path: keep, size: 1, mtime: 1, params: ""}
	s.notePreview(keep)
	s.cache.put(key2, &sdk.DocView{Path: keep, Name: "keep.md"})
	s.notePreview(keep)
	if _, ok := s.cache.get(key2); !ok {
		t.Fatal("窗口内文档的缓存不应失效")
	}
}

// TestAssetEvictionKeepsCurrentPreviewAssets 端到端语义:预览一个带资产的文档后,
// 再预览其他文档直到窗口滚动,当前文档资产仍在(取回可用)。
func TestAssetEvictionKeepsCurrentPreviewAssets(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	doc := writeFile(t, dir, "cur.md", []byte("# 标题\n\n![x](img.png)\n"))
	img := writeFile(t, dir, "img.png", []byte{0x89, 'P', 'N', 'G'})
	id := s.RegisterFileAsset(doc, img, "image/png")
	if _, err := s.Preview(context.Background(), sdk.DocRequest{Path: doc}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxAssetDocs-1; i++ {
		other := writeFile(t, dir, fmt.Sprintf("o%d.txt", i), []byte("x"))
		if _, err := s.Preview(context.Background(), sdk.DocRequest{Path: other}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := s.assets[id]; !ok {
		t.Fatal("窗口内(最近预览)文档的资产应可取回")
	}
}
