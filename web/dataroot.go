// A-5#125 数据根可写性探测(web 侧只读视图)。
//
// 背景:数据根被设成只读时,宿主启动期只落 WARN/ERROR 日志(`cmd/gah` 侧,#122),
// 浏览器/壳里的用户看不到 —— 表现是"发消息没反应、配置改了不生效"。
// 这里给 `/api/state` 一个可写性事实,前端据此出**页内提示条**(不再只有日志)。
package web

import (
	"os"
	"sync"
	"time"
)

// probeWritableTTL 探针结果的复用时长。`/api/state` 是高频轮询端点(Web 端 3s 一次、
// 桌面壳另有 2s 一次自己的轮询),而每次都真去建删一个临时文件 = 高频小 IO。
//
// 代价与边界:数据根的可写性若在这 5s 内被改掉(比如用户 chmod),最迟 5s 后才反映出来。
// 这是 UI 提示条不是判据(真正拦截按 policy-guard 在每次工具调用时现探),所以可接受。
// 不可缓存的是「root 为空」那一支 —— 那是环境态不是探测结果,仍每次现算(它不碰盘)。
const probeWritableTTL = 5 * time.Second

type writableProbe struct {
	mu      sync.Mutex
	ttl     time.Duration
	root    string
	ok      *bool
	at      time.Time
	nowFunc func() time.Time // 测试注入时钟
}

var writeProbe = &writableProbe{ttl: probeWritableTTL, nowFunc: time.Now}

// probeWritable 试建即删一个探针文件判定数据根可写性(带 TTL 复用,见 probeWritableTTL)。
// 返回 nil = **无法判定**(root 为空:不经 cmd/gah 的嵌入/单测场景)→ 调用方省略字段,
// 不谎报可写(前端也就不会弹一条假的提示条)。
func probeWritable(root string) *bool {
	if root == "" {
		return nil
	}
	return writeProbe.get(root)
}

func (p *writableProbe) get(root string) *bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.nowFunc()
	if p.ok != nil && p.root == root && now.Sub(p.at) < p.ttl {
		return p.ok
	}
	ok := probeWritableOnce(root)
	p.root, p.ok, p.at = root, ok, now
	return ok
}

// probeWritableOnce 实探一次(无缓存)。单独一层是为了让缓存逻辑与「怎么做探测」分开。
func probeWritableOnce(root string) *bool {
	f, err := os.CreateTemp(root, ".gah-write-probe-")
	if err != nil {
		no := false
		return &no
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name) // 探针不残留:失败也无所谓(只读根本来就不该有产物)
	yes := true
	return &yes
}

// dataRootPath 数据根路径(GAH_HOME 由 boot 恒定 Setenv;未注入时返回空 →
// 状态字段省略,不把 cwd 当成数据根)。
func dataRootPath() string {
	return os.Getenv("GAH_HOME")
}
