// 白名单的人工出口:给**已经放在 plugins/ 里**的二进制补登记 / 移除登记。
//
// 为什么单独一个文件而不是塞进 install.go:这两条命令的操作对象是**盘上已有的东西**
// (可能是用户手工拷的、可能是别人给的一整个目录),而 install 走的是「拉取→构建→落位」。
// 两条路的信任语义也不同:install 登记的是**自己刚构建出来的那一份**,trust 登记的是
// **用户手上已经有的那一份** —— 后者更要紧的是让人先看清楚再敲。
package install

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
)

// pluginsDir 数据根下的插件目录(与 installBridge 落位一致)。
func pluginsDir(home string) string { return filepath.Join(home, "plugins") }

// findPluginBin 在两种布局里定位二进制:扁平 plugins/<名>[.exe]、发布布局 plugins/<名>/<名>[.exe]。
func findPluginBin(dir, name string) (string, bool) {
	for _, cand := range []string{
		filepath.Join(dir, name),
		filepath.Join(dir, name+".exe"),
		filepath.Join(dir, name, name),
		filepath.Join(dir, name, name+".exe"),
	} {
		if fileExists(cand) {
			return cand, true
		}
	}
	return "", false
}

// Trust 把盘上已有的插件二进制登记进白名单。
//
// 刻意**不做**任何「哈希不符就改写」的逻辑:那个能力属于 install(它担保的是自己刚构建的
// 产物)。这里若发现清单里已有**不同**的哈希,显式报错并让人自己决定 —— 自动洗白一个
// 对不上的哈希,等于把「文件被换掉了」这件事抹掉。
func Trust(name, home string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("trust-plugin: 缺少插件名")
	}
	dir := pluginsDir(home)
	bin, ok := findPluginBin(dir, name)
	if !ok {
		return fmt.Errorf("trust-plugin: %s 下找不到 %s[.exe](扁平或 %s/%s 两种布局都没找到)", dir, name, name, name)
	}
	sum, err := plugintrust.HashFile(bin)
	if err != nil {
		return fmt.Errorf("trust-plugin: 算 %s 的哈希失败: %w", bin, err)
	}
	list, err := plugintrust.Load(dir)
	if err != nil {
		return err
	}
	if cur, ok := list.Sum(name); ok && cur != sum {
		return fmt.Errorf("trust-plugin: %s 在白名单里已有**不同**的哈希(文件可能已被改动)。"+
			"确认这就是你要的版本后,先 gah -untrust-plugin %s 再执行本命令", name, name)
	}
	return list.Record(name, sum)
}

// Untrust 从白名单移除一个条目(不删二进制)。
func Untrust(name, home string) error {
	list, err := plugintrust.Load(pluginsDir(home))
	if err != nil {
		return err
	}
	return list.Remove(strings.TrimSpace(name))
}

// TrustedList 白名单里的插件名(排序)。
func TrustedList(home string) ([]string, error) {
	list, err := plugintrust.Load(pluginsDir(home))
	if err != nil {
		return nil, err
	}
	return list.Names(), nil
}
