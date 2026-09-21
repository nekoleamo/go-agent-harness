// A-1 两项**人眼项**的素材生成器(不属于自动断言,默认跳过):
//
//	A-1a #42 P5 整体视觉(深灰底 / ▍ 竖线 / 折叠 ▲ / 思考色 / Ctrl+O 展开 / 80 列不错位)
//	A-1b #39 导出 HTML 观感(会话导出为自包含网页后,与 TUI 同色系、思考块不丢、长输出不溢出)
//
// 这两项按口径(不假勾)只能人看,自动化能做的只有"把要看的东西摆到桌上":本用例用
// profile `acctui`(llm-mock)跑一个含 思考块 + 工具调用 + 长输出 + 收尾文本 的回合,
// 把屏幕、带色原始流、`/export` 真产物一起写到 GAH_EYE_DIR,并附 README(怎么看/判据/失败长相)。
//
// 复跑:
//
//	GAH_EYE_DIR=~/gah-acceptance/human-eye go test ./tests/ -run TestTUIAcceptHumanEyeMaterials -count=1 -v
package tests

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// eyePatch 素材回合脚本:思考增量 → shell 工具(40 行长输出,测折叠/展开)→ 收尾文本。
const eyePatch = `entries:
  - id: llm-openai-compat
    enabled: false
  - id: llm-mock
    enabled: true
    data:
      script: '[{"thinking":"先想一下:这是人眼检查素材的推理段,应当用区别于正文的颜色显示,折叠时只留头部。","tool":{"name":"shell","args":"{\"command\":\"i=1; while [ $i -le 40 ]; do echo 长输出第 $i 行:用于检查折叠与 Ctrl+O 展开后的对齐; i=$((i+1)); done\"}"}},{"text":"素材回合结束:请对照 README 检查 ▍ 竖线、折叠 ▲、思考色与展开后的对齐。"}]'
`

// eyeReadme 素材目录自述(A-1a/A-1b 两项人眼项的检查清单)。
const eyeReadme = `# 人眼素材(A-1a #42 整体视觉 / A-1b #39 导出 HTML 观感)

生成命令(可随时重放,内容与仓库测试同源):

    GAH_EYE_DIR=~/gah-acceptance/human-eye go test ./tests/ -run TestTUIAcceptHumanEyeMaterials -count=1 -v

素材里有什么:

| 文件 | 用途 |
| --- | --- |
| 01-回合后-屏幕.txt | 一个完整回合(思考块 + 工具调用 + 收尾文本)的**当前屏幕**纯文本 |
| 01-回合后-原始流.ansi.txt | 同一回合的原始字节(带 ANSI 颜色):用 less -R 或 cat -v 看颜色码;人眼配色仍以真机为准 |
| 02-CtrlO-展开工具结果-屏幕.txt | Ctrl+O 展开最近一条工具结果(长输出 40 行)后的屏幕:看**对齐/不溢出/不粘连** |
| 03-CtrlT-思维块-屏幕.txt | Ctrl+T 展开思维块后的屏幕:看**思考段颜色与正文区分**、换行正确 |
| 04-会话导出.html | /export <路径>.html 的**真产物**(自包含单文件),浏览器打开看 #39 观感 |

注:03 与 02 可能一致 —— 素材的思维段很短(折叠态≈全文),Ctrl+T 无视觉变化属正常;
要看折叠/展开差异,请用真推理 provider(或把脚本里的 thinking 加长)重放素材。

两条重要口径:

1. pty 固定 **80 列 × 24 行** → 素材天然是"窄终端"那一档(换行/错位最容易暴露)。
2. 屏幕 *.txt 来自**最小屏幕模型**(不处理 ESC[K),会残留上一帧字符 → 只看**内容与顺序**
   (有哪些元素、折没折叠、谁在谁上面);**对齐、颜色、间距一律以真终端为准**。
   配色无法从文本文件判断 —— 素材是"对照参考",人眼判据必须在真终端里跑一次(见下)。

---

## A-1a #42 P5 整体视觉(在真终端里看)

前置:gah(TUI)+ 任一 provider(素材用 llm-mock 也能出全部元素:思考块/工具/折叠)。

做什么 → 看什么 → 判据(通过):

1. 起 TUI 跑一个小回合 → **深灰底不刺眼**;用户消息左侧 ▍ 竖线**不间断**、与正文有间距。
2. 工具结果行默认折叠 → ▲ 折叠标记与流内缩进对齐;Ctrl+O 展开后**长输出不串行**(40 行素材可对照 02-*.txt)。
3. Ctrl+T 思维块 → 思考段用**区别于正文的语义色**;折叠态只留头部、展开态换行正确。
4. Ctrl+↑(滚动摘要区)→ 摘要区可读、不与状态栏重叠。
5. 把终端缩到 **80 列** → 无折行错位、无竖线断裂。

失败长什么样:▍ 断在某个消息类型上 / ▲ 与正文错位 / Ctrl+O 后行粘连(无空格分隔)/
思考色与正文同色 / 窄终端下状态栏被正文压住 / 颜色刺眼或发灰到看不清(对比度不足)。

## A-1b #39 导出 HTML 观感

前置:一个含 思考块 + 工具调用 + 长输出的会话(素材回合已满足)。

做什么 → 看什么 → 判据(通过):

1. 浏览器打开 04-会话导出.html → 与 TUI **同一色系**(深底/浅字,强调色一致,不出现默认紫渐变或 emoji 装饰)。
2. 找思考块 → **不丢内容**(折叠态也能展开看到全文);正文/工具/思考**三类块视觉可区分**。
3. 看长输出(40 行素材)→ 不溢出容器(无横向滚动条把页面撑破),等宽字体对齐。
4. 看中文 → **无乱码**(UTF-8 正常),标点/宽度正常。

失败长什么样:思考块整段丢失或只剩头 / 工具输出溢出容器 / 中文变方框或乱码 /
配色与 TUI 不一致(比如亮白底) / 出现 emoji 或与 harness 无关的装饰。

---

勾完把结论回填 docs/VERIFY.md 的 A 表相应行(#39 / #42)与「人工验证清单」对应行的 [x]。
`

func TestTUIAcceptHumanEyeMaterials(t *testing.T) {
	eyeDir := os.Getenv("GAH_EYE_DIR")
	if eyeDir == "" {
		t.Skip("设置 GAH_EYE_DIR=<目录> 才跑(人眼素材生成,不参与门禁)")
	}
	if err := os.MkdirAll(eyeDir, 0o755); err != nil {
		t.Fatalf("建素材目录失败: %v", err)
	}
	bin, env, root, _ := tuiAcceptSetup(t)
	writeAcceptFile(t, filepath.Join(root, "config"), "patch-acctui.yaml", eyePatch)

	s := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	save := func(name, content string) {
		if err := os.WriteFile(filepath.Join(eyeDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写 %s 失败: %v", name, err)
		}
		t.Logf("已写 %s(%d 字节)", name, len(content))
	}

	// 回合:思考 + 工具(40 行输出)+ 收尾文本
	s.send("跑个人眼素材回合\r")
	if !s.waitRaw("素材回合结束", 25*time.Second) {
		t.Fatalf("素材回合未完成;屏尾 %q", tailS(stripANSI(s.screen()), 600))
	}
	time.Sleep(800 * time.Millisecond)
	save("01-回合后-屏幕.txt", stripANSI(s.screen()))
	save("01-回合后-原始流.ansi.txt", s.rawText())

	// Ctrl+O:展开最近一条工具结果(长输出对齐的检查点)
	s.send("\x0f")
	time.Sleep(900 * time.Millisecond)
	save("02-CtrlO-展开工具结果-屏幕.txt", stripANSI(s.screen()))

	// Ctrl+T:思维块展开(思考色与换行的检查点)
	s.send("\x14")
	time.Sleep(900 * time.Millisecond)
	save("03-CtrlT-思维块-屏幕.txt", stripANSI(s.screen()))

	// /export 真产物(自包含 HTML;浏览器直接打开看观感)
	htmlPath := filepath.Join(eyeDir, "04-会话导出.html")
	s.send("/export " + htmlPath + "\r")
	if !s.waitRaw("已导出", 15*time.Second) {
		t.Fatalf("/export 未完成;屏尾 %q", tailS(stripANSI(s.screen()), 300))
	}
	time.Sleep(500 * time.Millisecond)
	st, err := os.Stat(htmlPath)
	if err != nil || st.Size() == 0 {
		t.Fatalf("/export 产物缺失或为空: %v", err)
	}
	t.Logf("已写 04-会话导出.html(%d 字节)", st.Size())

	// 交给人的一页纸(怎么看 / 判据 / 失败长相)
	save("README.md", eyeReadme)
}
