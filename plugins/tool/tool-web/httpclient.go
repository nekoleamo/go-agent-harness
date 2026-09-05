// 共享 HTTP client(M6.14):web_fetch 与 web_search 复用同一 client——
// 统一 30s 超时/UA/响应体上限,避免各自造轮子导致超时口径不一致。
package toolweb

import (
	"net/http"
	"time"
)

const (
	fetchTimeout = 30 * time.Second
	maxBody      = 1 << 20 // 1MB 响应体上限
)

// userAgent 统一 UA(fetch/search 同源,便于目标站识别与限流配额)。
const userAgent = "gah-web-tool/1.0 (go-agent-harness)"

// newHTTPClient 构造共享 client。
func newHTTPClient() *http.Client {
	return &http.Client{Timeout: fetchTimeout}
}
