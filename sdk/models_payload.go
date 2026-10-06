// models_payload.go:`/models` 响应解析(**单一事实源**)。
//
// 为什么单独一份而不是各写各的:同一条 /models 响应有两条消费路径 —— 适配器自身的
// ListModels(TUI /model 用)与 host-llm 对非活跃端点的聚合(OpenAIFetchModels)。两处各解一次
// 就会出现「列表里有徽标、聚合列表里没有」这种漂移,而徽标是判定「能不能当 agent 用」的依据
// (见 ModelVerdict),漂移的后果是用户看见的结论互相矛盾。
//
// 字段是**逐个端点可选**的,解不出来就留空而不是报错:
//   - 标准 OpenAI 形状只有 id / owned_by;
//   - OpenRouter 这类聚合站额外给 context_length / supported_parameters / architecture /
//     top_provider.max_completion_tokens / pricing。
//
// 实测同一份字段名在不同端点的**类型**也不同(pricing 可能是字符串 "0" 也可能是数字),
// 所以按 RawMessage 逐个宽容解析。
package sdk

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ParseModelsPayload 解析 OpenAI 兼容 /models 响应为 []ModelInfo。
// 结构不符(整体不是 JSON / data 不是数组)显式报错;单个条目缺字段不算错(那是端点没给)。
func ParseModelsPayload(raw []byte) ([]ModelInfo, error) {
	var mr struct {
		Object string            `json:"object"`
		Data   []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &mr); err != nil {
		return nil, fmt.Errorf("解析失败: %w", err)
	}
	out := make([]ModelInfo, 0, len(mr.Data))
	for _, item := range mr.Data {
		var d modelsDataItem
		if err := json.Unmarshal(item, &d); err != nil {
			continue // 单条坏数据不该让整个列表失败(端点混进了别的形状也常见)
		}
		if strings.TrimSpace(d.ID) == "" {
			continue
		}
		info := ModelInfo{
			ID:              d.ID,
			OwnedBy:         d.OwnedBy,
			ContextWindow:   d.ContextLength,
			InputModalities: append([]string(nil), d.architectureInputModalities()...),
		}
		if d.TopProvider != nil {
			info.MaxOutputTokens = d.TopProvider.MaxCompletionTokens
		}
		// supported_parameters 里有 tools 才置真;没有该字段 ⇒ SupportsTools 保持 nil(三态)。
		if d.SupportedParameters != nil {
			supports := false
			for _, p := range d.SupportedParameters {
				if strings.EqualFold(strings.TrimSpace(p), "tools") {
					supports = true
					break
				}
			}
			info.SupportsTools = &supports
		}
		if v, ok := d.promptPrice(); ok {
			info.PromptPrice, info.PriceKnown = v, true
		}
		out = append(out, info)
	}
	return out, nil
}

// modelsDataItem /models 单条(只声明**已被证实存在**的字段,其余留空)。
type modelsDataItem struct {
	ID                  string   `json:"id"`
	OwnedBy             string   `json:"owned_by"`
	ContextLength       int      `json:"context_length"`
	SupportedParameters []string `json:"supported_parameters"`
	Architecture        *struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	TopProvider *struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	Pricing *struct {
		// RawMessage:实测同一字段在不同端点可能是 "0" 字符串、0 数字,或 null。
		Prompt json.RawMessage `json:"prompt"`
	} `json:"pricing"`
}

func (d modelsDataItem) architectureInputModalities() []string {
	if d.Architecture == nil {
		return nil
	}
	return d.Architecture.InputModalities
}

// promptPrice 输入价(每百万 token 美元)。ok=false = 端点没给价格。
func (d modelsDataItem) promptPrice() (float64, bool) {
	if d.Pricing == nil || len(d.Pricing.Prompt) == 0 {
		return 0, false
	}
	raw := strings.TrimSpace(string(d.Pricing.Prompt))
	if raw == "null" || raw == `""` {
		return 0, false
	}
	// 引号包裹的数字先剥掉(OpenRouter 用字符串价)
	raw = strings.Trim(raw, `"`)
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}
