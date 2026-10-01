package hostintcmd

// /memory 命令面的行为契约(第一百零九批)。
//
// 用**真实插件装配**(host-system-prompt + host-memory),不用假实现:命令口径与插件
// 口径必须一起验 —— 假实现会让"序号到底按哪个顺序"这类问题在测试里看不出来。
//
// 重点:写入只由人发起、List 的展示序(新的在前)与 rm 的序号一致、--from 按来源整段删、
// 没装插件时显式说没装。

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/plugins/host/host-memory"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
)

func memHost(t *testing.T) *Host {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t)
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostmemory.Plugin{}).Start(c, nil); err != nil {
		t.Fatal(err)
	}
	return &Host{c: c}
}

func TestCmdMemoryAddListRemove(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"add", "交付前先跑一遍单测"})
	if err != nil || !strings.Contains(out, "已记住") {
		t.Fatalf("add: %v(%s)", err, out)
	}
	list, err := h.cmdMemory([]string{"list"})
	if err != nil || !strings.Contains(list, "交付前先跑一遍单测") {
		t.Fatalf("list 应看到刚记的: %v(%s)", err, list)
	}
	// 序号删除:List 里的编号
	if _, err := h.cmdMemory([]string{"rm", "1"}); err != nil {
		t.Fatalf("rm 1: %v", err)
	}
	list2, _ := h.cmdMemory([]string{"list"})
	if strings.Contains(list2, "交付前先跑一遍单测") {
		t.Fatalf("rm 后不应还在:\n%s", list2)
	}
	if !strings.Contains(list2, "还没有记忆") {
		t.Fatalf("删空后应显示空态:\n%s", list2)
	}
}

func TestCmdMemoryBareTextTreatedAsAdd(t *testing.T) {
	h := memHost(t)
	// 用户没打 add,直接说了内容 —— 善意当作 add(这是最自然的用法)
	if _, err := h.cmdMemory([]string{"周报里先写结论"}); err != nil {
		t.Fatalf("裸文本应被当作 add: %v", err)
	}
	list, _ := h.cmdMemory([]string{"list"})
	if !strings.Contains(list, "先写结论") {
		t.Fatalf("没记上:\n%s", list)
	}
}

func TestCmdMemoryRemoveBySource(t *testing.T) {
	h := memHost(t)
	_, _ = h.cmdMemory([]string{"add", "带来源的一条"})
	out, err := h.cmdMemory([]string{"rm", "--from", "sess-9"})
	if err != nil {
		t.Fatalf("--from 应可解析: %v", err)
	}
	// 手工记的没有来源,所以是"没有来自该会话的记忆" —— 回执要说清,不能谎称删掉了
	if !strings.Contains(out, "没有来自") {
		t.Fatalf("回执应如实说明没删到: %s", out)
	}
}

func TestCmdMemoryToggle(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"off"})
	if err != nil || !strings.Contains(out, "已关闭") {
		t.Fatalf("off: %v(%s)", err, out)
	}
	out2, err := h.cmdMemory([]string{"on"})
	if err != nil || !strings.Contains(out2, "开启") {
		t.Fatalf("on: %v(%s)", err, out2)
	}
}

func TestCmdMemoryStatusMentionsUsage(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory(nil)
	if err != nil {
		t.Fatalf("无参应给概览: %v", err)
	}
	for _, must := range []string{"/memory add", "/memory list", "/memory rm", "预算"} {
		if !strings.Contains(out, must) {
			t.Fatalf("概览应含用法 %q:\n%s", must, out)
		}
	}
}

func TestCmdMemoryBadIndex(t *testing.T) {
	h := memHost(t)
	if _, err := h.cmdMemory([]string{"rm", "abc"}); err == nil {
		t.Fatal("非数字序号应报错")
	}
	if _, err := h.cmdMemory([]string{"add"}); err == nil {
		t.Fatal("add 缺内容应给用法")
	}
}

func TestMemoryCommandRegistered(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	_, cmds := newHost(t)
	if _, ok := cmds.Get("memory"); !ok {
		t.Fatal("命令未注册: /memory")
	}
}

// TestCmdMemoryProjectView 项目级记忆为空时应说清"没有"而不是空白。
func TestCmdMemoryProjectView(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"project"})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if !strings.Contains(out, "还没有项目级记忆") {
		t.Fatalf("空项目记忆应如实说明:\n%s", out)
	}
}

// TestCmdMemoryRemoveFromMissingSourceValue --from 后面没给会话 ⇒ 给用法(不静默删)。
func TestCmdMemoryRemoveFromMissingSourceValue(t *testing.T) {
	h := memHost(t)
	if _, err := h.cmdMemory([]string{"rm", "--from"}); err == nil {
		t.Fatal("--from 缺会话应报错并给用法")
	}
}

// TestCmdMemoryListEmptyGivesHint 空列表时给一句"怎么用",不是空白(对齐其它命令口径)。
func TestCmdMemoryListEmptyGivesHint(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "/memory add") {
		t.Fatalf("空列表应告诉用户怎么记:\n%s", out)
	}
}
