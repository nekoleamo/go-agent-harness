package sdk

import "testing"

// 会话级偏好的统一回落(第一百一十六批):没单独设过的项跟随全局当前值,
// 且每项的来源可查(UI 要靠它区分"跟随全局"与"本页签独立")。
func TestResolveSessionPrefsFallsBackPerField(t *testing.T) {
	session := SessionPrefs{Role: "finance"} // 只设了角色
	global := SessionPrefs{Role: "base", Model: "gpt-x", Thinking: "high", Sandbox: "workspace-write", Approval: "smart"}
	got := ResolveSessionPrefs(session, global)

	if got.Role != "finance" || !got.FromSessionOf("role") {
		t.Fatalf("会话设了的角色应胜出: %+v", got)
	}
	if got.Model != "gpt-x" || got.FromSessionOf("model") {
		t.Fatalf("没设的模型应回落全局且来源=全局: %+v", got)
	}
	if got.Thinking != "high" || got.Sandbox != "workspace-write" || got.Approval != "smart" {
		t.Fatalf("其余三项都应回落全局: %+v", got)
	}
	// 全局也空 ⇒ 生效值为空(基线),不是发明的默认值
	empty := ResolveSessionPrefs(SessionPrefs{}, SessionPrefs{})
	if empty.Role != "" || empty.FromSessionOf("role") {
		t.Fatalf("两边都空应得空值且来源为全局: %+v", empty)
	}
}

// 反向验证的锚点:回落必须**逐项**进行,不能整份取一边。
// (把某个"会话设了"与"全局没设"的组合丢掉一项,上面那条就会红)
func TestResolveSessionPrefsEmptyStringMeansUnset(t *testing.T) {
	// 显式设成空串 = "没设"(这是回落能成立的前提,写进测试防止将来改成"空串=清空")
	got := ResolveSessionPrefs(SessionPrefs{Model: ""}, SessionPrefs{Model: "gpt-x"})
	if got.Model != "gpt-x" {
		t.Fatalf("空串应表示跟随,得 %q", got.Model)
	}
}
