// 内存 LRU 缓存(对齐 DOC_PREVIEW_PLAN §8「缓存」):默认 32 条目 / 64MiB,默认不落盘。
// key = (path, size, mtime, 参数签名);源文件变更(mtime/size)自动失效。
package hostdocview

import (
	"container/list"
	"fmt"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

type cacheKey struct {
	path   string
	size   int64
	mtime  int64
	params string
}

type cacheEntry struct {
	key  cacheKey
	view *sdk.DocView
	size int64
}

type docCache struct {
	mu       sync.Mutex
	maxItems int
	maxBytes int64
	bytes    int64
	ll       *list.List // 前 = 最近使用
	items    map[cacheKey]*list.Element
	hits     int
	misses   int
}

func newDocCache(maxItems int, maxBytes int64) *docCache {
	if maxItems <= 0 {
		maxItems = 32
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	return &docCache{maxItems: maxItems, maxBytes: maxBytes, ll: list.New(), items: map[cacheKey]*list.Element{}}
}

func (c *docCache) get(k cacheKey) (*sdk.DocView, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[k]
	if !ok {
		c.misses++
		return nil, false
	}
	c.hits++
	c.ll.MoveToFront(el)
	return el.Value.(*cacheEntry).view, true
}

func (c *docCache) put(k cacheKey, v *sdk.DocView) {
	if v == nil {
		return
	}
	size := viewSize(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		c.bytes -= el.Value.(*cacheEntry).size
		c.ll.Remove(el)
		delete(c.items, k)
	}
	el := c.ll.PushFront(&cacheEntry{key: k, view: v, size: size})
	c.items[k] = el
	c.bytes += size
	for c.ll.Len() > c.maxItems || (c.bytes > c.maxBytes && c.ll.Len() > 1) {
		back := c.ll.Back()
		if back == nil {
			break
		}
		e := back.Value.(*cacheEntry)
		c.bytes -= e.size
		c.ll.Remove(back)
		delete(c.items, e.key)
	}
}

// paramSig 参数签名(可变维度进 key;零值不进,保证默认请求命中同一份)。
func paramSig(req sdk.DocRequest) string {
	return fmt.Sprintf("mb=%d/bl=%d/pg=%d/pgs=%v/sh=%d/na=%t/st=%t",
		req.MaxBytes, req.MaxBlocks, req.Page, req.Pages, req.Sheet, req.NoAssets, req.Strict)
}

// viewSize 粗略估算视图内存占用(块数 + 文本长度),够用即可。
func viewSize(v *sdk.DocView) int64 {
	n := int64(len(v.Blocks))*128 + int64(len(v.Name)+len(v.Path)+len(v.Title)+len(v.Author))
	for i := range v.Blocks {
		b := &v.Blocks[i]
		n += int64(len(b.Text)+len(b.Lang)) + 64
		for _, r := range b.Runs {
			n += int64(len(r.Text)+len(r.Link)) + 48
		}
		for _, h := range b.Head {
			n += int64(len(h)) + 8
		}
		for _, row := range b.Rows {
			for _, c := range row {
				n += int64(len(c.Text)) + 32
			}
		}
		if b.Asset != nil {
			n += 64
		}
	}
	return n
}
