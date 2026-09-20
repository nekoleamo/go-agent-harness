// A-5#125 数据根可写性探测(web 侧只读视图)。
//
// 背景:数据根被设成只读时,宿主启动期只落 WARN/ERROR 日志(`cmd/gah` 侧,#122),
// 浏览器/壳里的用户看不到 —— 表现是"发消息没反应、配置改了不生效"。
// 这里给 `/api/state` 一个可写性事实,前端据此出**页内提示条**(不再只有日志)。
package web

import (
	"os"
)

// probeWritable 试建即删一个探针文件判定数据根可写性。
// 返回 nil = **无法判定**(root 为空:不经 cmd/gah 的嵌入/单测场景)→ 调用方省略字段,
// 不谎报可写(前端也就不会弹一条假的提示条)。
func probeWritable(root string) *bool {
	if root == "" {
		return nil
	}
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
