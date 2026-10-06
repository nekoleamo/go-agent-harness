// 解析与判定的单测。样本取自 OpenRouter 真实响应形状(2026-10-06 实测),
// 而不是自造的"理想形状" —— 真实端点会缺字段、会把价格写成字符串、会在单条里混进坏数据。
package sdk

import "testing"

func boolPtr(b bool) *bool { return &b }

// TestParseModelsPayloadOpenRouterShape 真实形状:标准字段 + OpenRouter 的扩展字段。
func TestParseModelsPayloadOpenRouterShape(t *testing.T) {
	payload := []byte(`{"object":"list","data":[
      {"id":"nvidia/nemotron-3-ultra-550b-a55b:free","context_length":1000000,
       "supported_parameters":["tools","tool_choice","reasoning"],
       "architecture":{"input_modalities":["text"],"output_modalities":["text"]},
       "top_provider":{"max_completion_tokens":65536},
       "pricing":{"prompt":"0","completion":"0"}},
      {"id":"thinkingmachines/inkling:free","context_length":1048576,
       "supported_parameters":["tools"],
       "architecture":{"input_modalities":["text","image","audio"]},
       "top_provider":{"max_completion_tokens":262144},
       "pricing":{"prompt":"0","completion":"0"}},
      {"id":"nvidia/nemotron-3.5-content-safety:free","context_length":128000,
       "supported_parameters":["temperature"],
       "architecture":{"input_modalities":["text","image"]},
       "top_provider":{"max_completion_tokens":8192},
       "pricing":{"prompt":"0","completion":"0"}},
      {"id":"openrouter/free","context_length":200000,
       "supported_parameters":["tools","tool_choice"],
       "pricing":{"prompt":"0","completion":"0"}},
      {"id":"deepseek-chat","owned_by":"deepseek"},
      {"id":"paid/model-x","context_length":128000,
       "supported_parameters":["tools"],"pricing":{"prompt":"0.0000015","completion":"0.0000075"}}
    ]}`)
	got, err := ParseModelsPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("应解出 6 条,得 %d", len(got))
	}

	// ① 免费 + 工具 + 1M + 输出 64K ⇒ 可用,标签齐
	v := AssessModel(got[0])
	if !v.Free || !v.Tools || !v.Usable {
		t.Fatalf("nemotron-3-ultra 应免费且可用: %+v", v)
	}
	if v.ContextWindow != 1000000 || len(v.Tags) != 3 {
		// 3 个标签:免费 / 工具 / 256K 上下文 —— 实测该模型是 1,000,000 而非 1,048,576,
		// 所以「1M 上下文」这一档不适用(阈值必须是 ≥1048576,不是"一百万左右")。
		t.Fatalf("上下文/标签不对: %+v tags=%v", v, v.Tags)
	}
	if got[0].MaxOutputTokens != 65536 {
		t.Fatalf("输出上限没解出来: %d", got[0].MaxOutputTokens)
	}
	if got[0].VendorHint() != "nvidia" {
		t.Fatalf("owned_by 缺失时应回退 id 前缀,得 %q", got[0].VendorHint())
	}

	// ② 只声明 tools(无 tool_choice)也算支持 —— gah 自己不发 tool_choice,不能因此判死
	v = AssessModel(got[1])
	if !v.Tools || !v.Usable || !v.Vision {
		t.Fatalf("inkling 应判为「工具+看图+可用」: %+v", v)
	}

	// ③ 声明了但不含 tools ⇒ 明确不可用(这是内容安全分类器那类)
	v = AssessModel(got[2])
	if v.ToolsKnown == false || v.Tools || v.Usable {
		t.Fatalf("声明不支持 tools 的应判为不可用: %+v", v)
	}
	if v.Warn == "" {
		t.Fatal("不可用必须给一句人话原因,不能只靠徽标让人猜")
	}

	// ④ openrouter/free = 免费 + 工具 + 200K
	v = AssessModel(got[3])
	if !v.Free || !v.Usable || v.ContextWindow != 200000 {
		t.Fatalf("openrouter/free 判定不对: %+v", v)
	}

	// ⑤ 标准形状(只有 id/owned_by):**未知不等于不可用**,否则所有常规 provider 会被清空
	v = AssessModel(got[4])
	if v.ToolsKnown {
		t.Fatalf("没声明时 SupportsTools 应为 nil(三态),得 %+v", v)
	}
	if !v.Usable {
		t.Fatalf("未声明能力的模型不应被判死: %+v", v)
	}
	if v.Free {
		t.Fatal("没给价格的不能算免费(否则会把付费模型标成免费)")
	}
	if v.Warn != "" {
		t.Fatalf("未声明就不该报警告: %q", v.Warn)
	}

	// ⑥ 付费模型:价格解出来 ⇒ 不标免费
	v = AssessModel(got[5])
	if v.Free || !got[5].PriceKnown {
		t.Fatalf("付费模型的价格解析不对: %+v", got[5])
	}
}

// TestParseModelsPayloadTolerance 单条坏数据/缺字段不许让整表失败。
func TestParseModelsPayloadTolerance(t *testing.T) {
	payload := []byte(`{"data":[
      {"id":"good-model","context_length":128000},
      {"id":"","owned_by":"空 id 会被跳过"},
      "这不是对象",
      {"id":"no-context"}
    ]}`)
	got, err := ParseModelsPayload(payload)
	if err != nil {
		t.Fatalf("单条坏数据不该让整体失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应留下 2 条可用,得 %d(%+v)", len(got), got)
	}
	if _, err := ParseModelsPayload([]byte("not json")); err == nil {
		t.Fatal("整体不是 JSON 必须显式报错")
	}
}

// TestAssessModelWarnings 三类警告各出一次,且优先级是「会不会让活儿干不成」。
func TestAssessModelWarnings(t *testing.T) {
	// 无工具调用 → 最高优先
	v := AssessModel(ModelInfo{ID: "x:free", SupportsTools: boolPtr(false), ContextWindow: 1_000_000, PriceKnown: true})
	if v.Usable || v.Warn == "" {
		t.Fatalf("无工具调用应不可用且有原因: %+v", v)
	}

	// 上下文过小
	v = AssessModel(ModelInfo{ID: "small", SupportsTools: boolPtr(true), ContextWindow: 32 * 1024})
	if v.Usable {
		t.Fatal("上下文 32K 应判为不可用")
	}
	if v.Warn == "" {
		t.Fatal("上下文过小应有警告")
	}

	// 免费 + 输出上限低 → 截断警告
	v = AssessModel(ModelInfo{ID: "trunc:free", SupportsTools: boolPtr(true), ContextWindow: 128 * 1024,
		MaxOutputTokens: VerdictFreeOutputWarn - 1024, PriceKnown: true})
	if !v.Usable {
		t.Fatalf("够用就不该判死: %+v", v)
	}
	if v.Warn == "" {
		t.Fatal("输出上限偏低应有警告(截断发生在半句话上,比报错更难查)")
	}

	// 付费 + 输出上限低 ⇒ 不报截断警告(不是免费的特有坑)
	v = AssessModel(ModelInfo{ID: "paid", SupportsTools: boolPtr(true), ContextWindow: 128 * 1024,
		MaxOutputTokens: 4096, PromptPrice: 1, PriceKnown: true})
	if v.Warn != "" {
		t.Fatalf("付费模型不该带截断警告: %q", v.Warn)
	}
}

// TestPromptPriceIsPerToken 钉住价格的**单位**。
//
// 背景:字段名曾经叫 PromptPricePerMillion、注释也写"每百万 token",而端点给的是**每 token**
// (实测 OpenRouter pricing.prompt = 0.00000015 即 gpt-4o-mini 的 $0.15/M)。判定免费只比较
// <=0 所以功能没坏,但一个骗人的字段名会让读代码的人以为可以直接乘百万 —— 这条用例让改名
// 之后的事实有据可查:真实响应的解析结果原样保留端点给的值,不做任何换算。
func TestPromptPriceIsPerToken(t *testing.T) {
	payload := []byte(`{"data":[
      {"id":"paid/tiny","pricing":{"prompt":"0.00000015","completion":"0.0000006"}},
      {"id":"free/zero","pricing":{"prompt":"0","completion":"0"}}
    ]}`)
	got, err := ParseModelsPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].PromptPrice != 0.00000015 {
		t.Fatalf("应原样保留端点的每-token 值,不做换算: %v", got[0].PromptPrice)
	}
	if !got[0].PriceKnown {
		t.Fatal("给了价格就该标记为已知")
	}
	if AssessModel(got[0]).Free {
		t.Fatal("极小的非零价格不该被当成免费")
	}
	if !AssessModel(got[1]).Free {
		t.Fatal("价格为 0 应判为免费")
	}
}
