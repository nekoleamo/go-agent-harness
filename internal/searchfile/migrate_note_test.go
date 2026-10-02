package searchfile

import "testing"

// TestMigrateNotesMentionKeys 归属不明时提示里必须点名 api_key/exa_api_key
// （提示是唯一让用户知道"要手动挪一下"的渠道，含糊等于没说）。
func TestMigrateNotesMentionKeys(t *testing.T) {
	_, notes := migrate(File{Provider: ProviderAnysearch, APIKey: "k", Endpoint: "https://api.exa.ai/search"})
	if len(notes) != 2 {
		t.Fatalf("应有两条提示,got %v", notes)
	}
	for _, want := range []string{"api_key", "exa_api_key", "endpoint", "exa_endpoint", ProviderAnysearch} {
		found := false
		for _, n := range notes {
			if containsStr(n, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("提示未提到 %q: %v", want, notes)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
