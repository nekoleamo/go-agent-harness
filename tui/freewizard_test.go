// 多值自由参数逐步向导(A 通用)测试:/provider set 式多参数命令逐级 Enter 输入,
// 每步确认后提示下一步;尾可选步空回车跳过;一次多词/单参数命令保持旧语义。
package tui

import (
	"strings"
	"testing"
)

func TestFreeStepHints(t *testing.T) {
	// 单参数:旧文案(兼容)
	h := freeStepHints("/name", []string{"显示名"}, 0)
	if len(h) != 2 || !strings.Contains(h[0], "显示名") {
		t.Fatalf("单参数应旧文案: %v", h)
	}
	// 多参数:分步提示
	h = freeStepHints("/provider set", []string{"baseUrl", "apiKey", "model?"}, 0)
	if !strings.Contains(h[0], "第 1/3") || !strings.Contains(h[0], "baseUrl") || strings.Contains(h[0], "跳过") {
		t.Fatalf("首步提示: %v", h)
	}
	h = freeStepHints("/provider set", []string{"baseUrl", "apiKey", "model?"}, 2)
	if !strings.Contains(h[0], "第 3/3") || !strings.Contains(h[0], "回车跳过") {
		t.Fatalf("尾可选步应提示跳过: %v", h)
	}
}

// freeModel 构造带逐步向导状态与命令执行记录的 model。
func freeModel(input string, params []string, done int) (*Model, *[]string) {
	var executed []string
	m := &Model{
		state: &State{Input: input,
			Free: &freeStep{Cmd: "provider", Params: params, Base: 2, Done: done}},
		onCommand: func(cmd string) error { executed = append(executed, cmd); return nil },
	}
	return m, &executed
}

func TestFreeWizardSteps(t *testing.T) {
	params := []string{"baseUrl", "apiKey", "model?"}
	m, exec := freeModel("/provider set ", params, 0)
	// 第 1 步前空回车:拦截等待(不执行)
	m.submit()
	if len(*exec) != 0 {
		t.Fatal("空回车在必填步不应执行")
	}
	if m.state.Free == nil || m.state.Free.Done != 0 {
		t.Fatalf("应仍在第 1 步: %+v", m.state.Free)
	}
	if len(m.state.Suggestions) == 0 || !strings.Contains(strings.Join(m.state.Suggestions, " "), "baseUrl") {
		t.Fatalf("应提示 baseUrl: %v", m.state.Suggestions)
	}
	// 输入 baseUrl 回车 → 步进第 2 步
	m.state.Input = "/provider set https://api.x/v1 "
	m.submit()
	if len(*exec) != 0 || m.state.Free.Done != 1 {
		t.Fatalf("第 1 值回车应步进: done=%d exec=%v", m.state.Free.Done, *exec)
	}
	if !strings.Contains(strings.Join(m.state.Suggestions, " "), "apiKey") {
		t.Fatalf("应提示 apiKey: %v", m.state.Suggestions)
	}
	// 输入 apiKey → 第 3 步(尾可选)
	m.state.Input = "/provider set https://api.x/v1 sk-abc "
	m.submit()
	if len(*exec) != 0 || m.state.Free.Done != 2 {
		t.Fatalf("第 2 值回车应步进: done=%d", m.state.Free.Done)
	}
	if !strings.Contains(strings.Join(m.state.Suggestions, " "), "回车跳过") {
		t.Fatalf("尾步应提示回车跳过: %v", m.state.Suggestions)
	}
	// 尾可选:输入 model → 执行(3 值全)
	m.state.Input = "/provider set https://api.x/v1 sk-abc deepseek-x "
	m.submit()
	if len(*exec) != 1 || !strings.Contains((*exec)[0], "deepseek-x") {
		t.Fatalf("3 值填完应执行: %v", *exec)
	}
	if m.state.Free != nil {
		t.Fatal("执行后向导应清除")
	}
}

func TestFreeWizardSkipOptional(t *testing.T) {
	params := []string{"baseUrl", "apiKey", "model?"}
	m, exec := freeModel("/provider set https://api.x/v1 sk-abc ", params, 2)
	// 尾可选步无新词回车 = 跳过执行(不带 model)
	m.submit()
	if len(*exec) != 1 {
		t.Fatal("跳过应执行")
	}
	if strings.Contains((*exec)[0], "model") || m.state.Free != nil {
		t.Fatalf("跳过执行应为 2 值无 model: %q", (*exec)[0])
	}
}

func TestFreeWizardOnceMultiWordAndDetach(t *testing.T) {
	params := []string{"baseUrl", "apiKey", "model?"}
	// 一次输入两词(旧习惯)回车:步进到第 3 步等待,再回车跳过执行
	m, exec := freeModel("/provider set https://api.x/v1 sk-abc ", params, 0)
	m.state.Input = "/provider set https://api.x/v1 sk-abc "
	m.submit()
	if m.state.Free == nil || m.state.Free.Done != 2 {
		t.Fatalf("一次多词应步进到尾步: %+v", m.state.Free)
	}
	m.submit() // 空回车跳过
	if len(*exec) != 1 {
		t.Fatalf("多词+跳过应执行: %v", *exec)
	}
	// 命令变更:脱离向导正常执行
	m2, exec2 := freeModel("/other x ", []string{"baseUrl", "apiKey"}, 0)
	m2.state.Input = "/other x "
	m2.submit()
	if len(*exec2) != 1 || m2.state.Free != nil {
		t.Fatalf("命令变更应脱离向导执行: %v free=%+v", *exec2, m2.state.Free)
	}
}

// TestFreeSingleParamNotWizard 单参数自由命令(/name /search)不进向导:直接执行旧语义。
func TestFreeSingleParamNotWizard(t *testing.T) {
	m := &Model{state: &State{Input: "/name foo"}, onCommand: func(cmd string) error { return nil }}
	var executed bool
	m.onCommand = func(string) error { executed = true; return nil }
	m.submit()
	if !executed {
		t.Fatal("单参数命令应直接执行(Free nil)")
	}
}
