// 会话绑定(P1 会话绑定命令面):IM 会话(route)→ 宿主会话 id 的持久映射。
// 用户经 /new /session 把某 IM 聊天绑定到宿主会话(多会话记录 + switch 承接);
// 桥在回合前按绑定 Open(id)(绑定会话不存在 = 宿主删除 → 解绑回主会话)。
// 存储 $GAH_HOME/config/im-sessions.yaml(routeKey 含 \x00,JSON 转义可安全往返),
// 0600 原子写(tmp+rename),重启恢复。无记录/空 id = 主会话(跟随宿主当前)。
package im

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// bindStore route.Key() → 宿主会话 id(空 = 主会话)。并发安全。
type bindStore struct {
	mu   sync.Mutex
	path string // 持久化路径;空 = 仅内存(不落盘)
	m    map[string]string
}

// newBindStore 构造并尝试装载既有绑定(path 空 = 仅内存)。
func newBindStore(path string) *bindStore {
	b := &bindStore{path: path, m: make(map[string]string)}
	if path != "" {
		b.load()
	}
	return b
}

// Set 绑定会话;id 空 = 解绑(回主会话)。持久化 best-effort(坏路径不致命)。
func (b *bindStore) Set(key, id string) {
	b.mu.Lock()
	if id == "" {
		delete(b.m, key)
	} else {
		b.m[key] = id
	}
	data, err := json.Marshal(b.m)
	path := b.path
	b.mu.Unlock()
	if err != nil || path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// Get 绑定会话 id(无记录返回 "" = 主会话)。
func (b *bindStore) Get(key string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.m[key]
}

// load 装载既有绑定;坏文件忽略(空表——会话记录可重建,不致命)。
func (b *bindStore) load() {
	data, err := os.ReadFile(b.path)
	if err != nil {
		return
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	b.mu.Lock()
	b.m = m
	b.mu.Unlock()
}
