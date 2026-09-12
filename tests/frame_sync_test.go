// SSE 帧类型同源护栏:NOND-W4 加 `schedule` 帧时踩到的坑 ——
// WS 路径按帧 type 动态分发,EventSource 降级路径却走**白名单**(web-src/src/transport.ts),
// 漏登记 = 该帧在 SSE 降级时被静默丢弃(功能只在 WS 可用,本地不降级就永远发现不了)。
// 本护栏把「events.go 的 FrameXxx ↔ types.ts 联合类型 ↔ transport.ts 白名单」钉成集合相等。
package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	// FrameXxx = "value"(events.go 常量声明)。
	reFrameConst = regexp.MustCompile(`(?m)^\s*Frame[A-Za-z]+ = "([a-z]+)"`)
	// types.ts 的 FrameType 联合:'x' | 'y' | …(取到该 type 声明结束)。
	reFrameTypeUnion = regexp.MustCompile(`(?s)export type FrameType =([^;]+);`)
	reTSStringLit    = regexp.MustCompile(`'([a-z]+)'`)
	// transport.ts 的白名单数组:for (const t of ['a', 'b', ...])
	reTransportList = regexp.MustCompile(`(?s)for \(const t of \[([^\]]+)\]\)`)
)

func frameSet(t *testing.T, path string, re *regexp.Regexp, label string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败(工作目录须为仓库根的 tests/): %v", path, err)
	}
	m := re.FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("%s 未匹配到%s(源码结构已变,请更新本次护栏的正则)", path, label)
	}
	out := map[string]bool{}
	for _, s := range reTSStringLit.FindAllStringSubmatch(m[1], -1) {
		out[s[1]] = true
	}
	return out
}

func TestFrameTypesInSyncWithFrontend(t *testing.T) {
	// 1) Go 侧单一事实源:web/events.go 的 FrameXxx 字面值
	b, err := os.ReadFile(filepath.Join("..", "web", "events.go"))
	if err != nil {
		t.Fatalf("读取 web/events.go 失败: %v", err)
	}
	goFrames := map[string]bool{}
	for _, m := range reFrameConst.FindAllStringSubmatch(string(b), -1) {
		goFrames[m[1]] = true
	}
	if len(goFrames) < 5 {
		t.Fatalf("从 web/events.go 只解析到 %d 个帧类型(正则失配?):%v", len(goFrames), goFrames)
	}

	// 2) 前端两处镜像:类型联合(TS 校验)+ SSE 降级白名单(运行期)
	tsUnion := frameSet(t, filepath.Join("..", "web-src", "src", "types.ts"), reFrameTypeUnion, "FrameType 联合类型")
	tsList := frameSet(t, filepath.Join("..", "web-src", "src", "transport.ts"), reTransportList, "帧类型白名单数组")

	for _, c := range []struct {
		got  map[string]bool
		file string
	}{{tsUnion, "web-src/src/types.ts"}, {tsList, "web-src/src/transport.ts"}} {
		for f := range goFrames {
			if !c.got[f] {
				t.Fatalf("%s 缺帧类型 %q —— 该帧在%s被丢弃(WS 路径按类型动态分发,EventSource 降级路径依赖白名单)", c.file, f, c.file)
			}
		}
		for f := range c.got {
			if !goFrames[f] {
				t.Fatalf("%s 多出帧类型 %q —— web/events.go 无对应 FrameXxx(死配置)", c.file, f)
			}
		}
	}

	// 3) 白名单与类型联合必须同集(两处漂移同样致命:TS 报错或运行期丢帧)
	if strings.Join(sortedKeys(tsUnion), ",") != strings.Join(sortedKeys(tsList), ",") {
		t.Fatalf("types.ts 与 transport.ts 帧集合不一致:union=%v list=%v", sortedKeys(tsUnion), sortedKeys(tsList))
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
