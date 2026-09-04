// 适配器 provider 能力单测:Configure 校验/原子切换/ProviderInfo/端点拼接。
package llmopenai

import (
	"strings"
	"testing"
)

func TestConfigureValidatesAndSwitches(t *testing.T) {
	a := &Adapter{baseURL: "https://api.deepseek.com/v1", apiKey: "old"}
	if err := a.Configure("https://api.siliconflow.cn/v1", "sk-new"); err != nil {
		t.Fatal(err)
	}
	if a.baseURL != "https://api.siliconflow.cn/v1" || a.apiKey != "sk-new" {
		t.Fatalf("Configure 应原子切换: %s %s", a.baseURL, a.apiKey)
	}
	// 非法 URL:拒绝且不写入
	if err := a.Configure("api.siliconflow.cn/v1", "k"); err == nil {
		t.Fatal("非 http(s) 前缀应拒绝")
	}
	if a.baseURL != "https://api.siliconflow.cn/v1" {
		t.Fatalf("拒绝后不应改变: %s", a.baseURL)
	}
	// 尾斜杠归一
	if err := a.Configure("https://api.anthropic.com/v1/", "k2"); err != nil {
		t.Fatal(err)
	}
	if a.baseURL != "https://api.anthropic.com/v1" {
		t.Fatalf("尾斜杠应去除: %q", a.baseURL)
	}
}

func TestProviderInfoAndEndpoint(t *testing.T) {
	a := &Adapter{baseURL: "https://api.o.com/v1", apiKey: "k123"}
	u, k := a.ProviderInfo()
	if u != "https://api.o.com/v1" || k != "k123" {
		t.Fatalf("ProviderInfo 不符: %s %s", u, k)
	}
	if ep := a.endpoint(); ep != "https://api.o.com/v1/chat/completions" {
		t.Fatalf("端点拼接不符: %s", ep)
	}
	if c := a.credentials(); c != "k123" {
		t.Fatalf("凭据读取不符: %s", c)
	}
}

func TestConfigureEmptyKeyAllowed(t *testing.T) {
	a := &Adapter{baseURL: "https://api.x/v1", apiKey: "k"}
	// 本地端点(如 Ollama)无 key 也允许(仅 URL 校验)
	if err := a.Configure("http://localhost:11434/v1", ""); err != nil {
		t.Fatal(err)
	}
	if a.apiKey != "" || !strings.HasPrefix(a.endpoint(), "http://localhost:11434") {
		t.Fatalf("本地端点应可无 key: %q %q", a.apiKey, a.endpoint())
	}
}
