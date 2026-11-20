package main

// installGate:「下载并安装」的准入判定(2026-11-09)。
//
// 为什么这条值得单测:它是**两条入口共用**的唯一判定点(托盘菜单 + /shell/update-install),
// 而两条路径原先各写一份 —— 托盘那份根本没有便携判定(文件头注释却写着「便携版只查不装」)
// ⇒ 便携版从托盘点下去会真的去覆盖程序目录:只读 U 盘上以 wails 原文报错收场,而 HTTP
// 端点同一步骤给的是「请下载新包覆盖本目录」。同一件事两套说法,取决于用户点了哪个入口。
//
// 顺序也有语义:便携拒绝**先于**版本对账 —— 便携版连「要不要装」都不该问。

import (
	"strings"
	"testing"
)

func TestInstallGatePortableBlocksFirst(t *testing.T) {
	cases := []struct {
		name                    string
		portable, writable, off bool
		expect, seen            string
		want                    installAction
		wantMsgContains         string
	}{
		{"便携+只读目录", true, false, false, "0.9.0", "0.9.0", installPortableOnly, "下载新包覆盖本目录"},
		{"便携+应急开关关掉", true, true, true, "0.9.0", "0.9.0", installPortableOnly, "下载新包覆盖本目录"},
		// 便携 + 版本也漂移 ⇒ 仍然先报便携:连装不装都没定,谈版本没有意义
		{"便携优先于版本漂移", true, false, false, "0.9.0", "0.9.1", installPortableOnly, "便携版"},
		// 便携 + 目录可写 + 开关未关 ⇒ 允许(2026-10-29 实测:helper 等父进程退出再 rename)
		{"便携但可写", true, true, false, "0.9.0", "0.9.0", installProceed, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, msg := installGate(c.expect, c.seen, c.portable, c.writable, c.off)
			if got != c.want {
				t.Fatalf("准入结果应为 %q,得 %q(msg=%q)", c.want, got, msg)
			}
			if c.wantMsgContains != "" && !strings.Contains(msg, c.wantMsgContains) {
				t.Fatalf("提示应含 %q,得 %q", c.wantMsgContains, msg)
			}
		})
	}
}

// 版本对账:检查与点安装之间镜像换掉清单是**真发生过**的(2026-11-03 真机反馈
// 「显示 0.8.4、装上 0.8.5」),所以对不上必须停下来,而不是静默装另一个版本。
func TestInstallGateVersionDrift(t *testing.T) {
	if got, msg := installGate("0.8.4", "0.8.5", false, true, false); got != installVersionChanged {
		t.Fatalf("确认 0.8.4 而源已是 0.8.5 时应停下,得 %q(%q)", got, msg)
	} else if !strings.Contains(msg, "0.8.5") || !strings.Contains(msg, "0.8.4") {
		t.Fatalf("提示应同时点名两个版本,得 %q", msg)
	}
	// 一致 ⇒ 放行
	if got, msg := installGate("0.8.5", "0.8.5", false, true, false); got != installProceed {
		t.Fatalf("版本一致应放行,得 %q(%q)", got, msg)
	}
	// 空 expect(旧前端不传)⇒ 不做对账、放行 —— 不能因为旧前端就拒绝安装
	if got, _ := installGate("", "0.8.5", false, true, false); got != installProceed {
		t.Fatalf("未传 expect 时应放行(旧前端兼容),得 %q", got)
	}
}
