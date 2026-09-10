// 内存 LRU 缓存单测:命中/失效/容量淘汰/参数隔离。
package hostdocview

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func view(path string, n int) *sdk.DocView {
	return &sdk.DocView{Path: path, Blocks: []sdk.DocBlock{{Kind: sdk.DocBlockCode, Text: strings.Repeat("x", n)}}}
}

func TestCacheLRU(t *testing.T) {
	c := newDocCache(2, 1<<20)
	k1 := cacheKey{path: "a", params: "p"}
	k2 := cacheKey{path: "b", params: "p"}
	k3 := cacheKey{path: "c", params: "p"}
	c.put(k1, view("a", 1))
	c.put(k2, view("b", 1))
	if _, ok := c.get(k1); !ok { // a 变最近使用
		t.Fatal("k1 应命中")
	}
	c.put(k3, view("c", 1)) // 淘汰最久未用 → b
	if _, ok := c.get(k2); ok {
		t.Fatal("k2 应已被淘汰")
	}
	if _, ok := c.get(k1); !ok {
		t.Fatal("k1 应保留")
	}
	if _, ok := c.get(k3); !ok {
		t.Fatal("k3 应保留")
	}
}

func TestCacheByteEviction(t *testing.T) {
	c := newDocCache(100, 4096)
	for i := 0; i < 20; i++ {
		c.put(cacheKey{path: string(rune('a' + i))}, view("x", 1024))
	}
	if c.ll.Len() >= 20 {
		t.Fatalf("应按字节上限淘汰,当前 %d 条", c.ll.Len())
	}
	if c.bytes > c.maxBytes {
		t.Fatalf("字节占用超限: %d > %d", c.bytes, c.maxBytes)
	}
}

func TestCacheParamIsolation(t *testing.T) {
	c := newDocCache(4, 1<<20)
	a := cacheKey{path: "a", params: paramSig(sdk.DocRequest{})}
	b := cacheKey{path: "a", params: paramSig(sdk.DocRequest{MaxBlocks: 10})}
	if a == b {
		t.Fatal("不同参数应产生不同 key")
	}
	c.put(a, view("a", 1))
	if _, ok := c.get(b); ok {
		t.Fatal("参数不同不应命中")
	}
}
