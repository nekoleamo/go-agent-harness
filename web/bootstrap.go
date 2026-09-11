// token 模式的浏览器引导页与访问地址构造(R10 未闭环 ③ 收口)。
//
// 背景:此前 token 只保护 /api/*;静态资源、/attachments/(用户附件)、/ui-plugins/ 均无鉴权,
// 且入口页会把 token 无条件写进 cookie —— 同机任意进程或一次跨站导航即可拿到 Web UI 与附件。
//
// 现在改为全表面鉴权(见 server.go authMiddleware):
//   - 缺凭据访问 /api/*  → 401;
//   - 缺凭据访问其它路径(导航/静态/附件/UI 插件产物)→ 返回本文件的引导页;
//   - 引导页从 URL fragment 取 `#token=...`,POST /api/auth 换取 SameSite=Strict 的
//     HttpOnly cookie,随后 location.replace("/") 抹掉 fragment。
//
// 为什么用 fragment:浏览器不会把 fragment 发往服务端,故它不进访问日志、不进 Referer、
// 也不会出现在服务器端任何记录里(仅留在本机浏览器地址栏与历史,与本地 config 同等信任域)。
// 引导页本身不含任何业务数据,也不引用 web/dist 资源。
package web

import (
	"io"
	"net/http"
	"net/url"
)

// authPath token 引导通道:唯一豁免鉴权门的路径(自身仍需有效 token 才能换 cookie)。
const authPath = "/api/auth"

// authBodyLimit 引导通道请求体上限(token 很短;防无界读)。
const authBodyLimit = 8 << 10 // 8 KiB

// bootstrapHTML 引导页(纯内联 HTML/JS:零外部资源、零业务数据;仅做「取 fragment → 换 cookie → 抹 fragment」)。
const bootstrapHTML = `<!doctype html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>gah · 需要访问凭据</title>
<style>
body{margin:0;display:flex;min-height:100vh;align-items:center;justify-content:center;background:#0f1115;color:#e6e8eb;font:14px/1.7 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",sans-serif}
main{max-width:600px;padding:32px}
h1{margin:0 0 12px;font-size:18px;font-weight:600}
p{margin:0;color:#a6adb8;word-break:break-all}
</style>
</head>
<body>
<main>
<h1>gah 需要访问凭据</h1>
<p id="msg">正在校验访问凭据…</p>
</main>
<script>
(function () {
  var msg = document.getElementById("msg");
  function show(text) { msg.textContent = text; }
  // 换到 cookie 后回到原请求(保留 path 与 query,如桌面壳的 ?shell=desktop;只去掉 fragment)
  var target = window.location.pathname + window.location.search;
  // token 取到 fragment 末尾:token 本身可能含 &(整段 fragment 由 FragmentURL 生成为 token=<token>),
  // 用 [\s\S]*$ 而非 [^&]*,避免含 & 的 token 被截断(桌面壳侧会先做 RFC3986 转义)。
  var hash = window.location.hash || "";
  var m = /(?:^#token=|[#&]token=)([\s\S]*)$/.exec(hash);
  if (!m || !m[1]) {
    show("未授权:请用启动 gah 时输出的访问地址(含 #token=…)打开本页;或从 $GAH_HOME/config/web.yaml 取得 token 后访问 http://<地址>/#token=<token>。");
    return;
  }
  var token = decodeURIComponent(m[1]);
  fetch("/api/auth", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token: token })
  }).then(function (resp) {
    if (resp.status === 204) { window.location.replace(target); return; }
    throw new Error("HTTP " + resp.status);
  }).catch(function (e) {
    show("访问凭据校验失败(" + e.message + "):请核对 token 后重新打开带 #token= 的地址。");
  });
})();
</script>
</body>
</html>
`

// writeBootstrap 输出引导页(200 + no-store;安全头按页收窄到「零外部资源 + 仅本页脚本」)。
func writeBootstrap(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	// 覆盖护栏的通用 CSP:引导页只需内联样式/脚本 + 同源 fetch,其余一律禁
	h.Set("Content-Security-Policy",
		"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, bootstrapHTML)
}

// FragmentURL 构造带引导 fragment 的访问地址(token 模式):凭据放 fragment,
// 由引导页换取 cookie;fragment 不会发往服务端。token 空 = 原样返回。
// fragment 用 url.URL 统一转义(引导页侧用 decodeURIComponent 还原,不用 + 语义)。
func FragmentURL(rawURL, token string) string {
	if token == "" || rawURL == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return rawURL // 非法输入:不猜,原样返回(调用方拿到的日志即可发现问题)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	u.Fragment = "token=" + token
	return u.String()
}
