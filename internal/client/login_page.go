package client

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
)

// This page confirms receipt of the one-time code, not a completed token exchange.
// All assets are inline and the CSP permits only this exact stylesheet.
func writeLoginCallbackPage(w http.ResponseWriter) {
	digest := sha256.Sum256([]byte(loginCallbackStyle))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'sha256-"+base64.StdEncoding.EncodeToString(digest[:])+"'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	_, _ = io.WriteString(w, loginCallbackHTML)
}

const loginCallbackStyle = `
:root{font-family:"Segoe UI","Microsoft YaHei UI",sans-serif;color:#e9edf4;background:#0c111a;color-scheme:dark;font-synthesis:none}
*{box-sizing:border-box}body{margin:0;min-height:100vh;min-height:100svh;display:grid;place-items:center;padding:32px 20px;background:radial-gradient(ellipse at 50% 15%,#27273f66,transparent 65%)}
main{width:min(100%,520px);padding:40px 36px 32px;text-align:center;background:#141c29;border:1px solid #30364b;border-radius:24px;box-shadow:0 24px 80px #0003}
.brand{display:flex;align-items:center;justify-content:center;gap:12px;margin-bottom:32px}.brand svg{width:42px;height:42px}.wordmark{text-align:left;font-size:22px;font-weight:700;letter-spacing:2px}.wordmark small{display:block;font-size:8px;font-weight:400;letter-spacing:1.8px;color:#8b9ab1;margin-top:3px}
.confirmation{display:grid;place-items:center;width:64px;height:64px;margin:0 auto 20px;border-radius:50%;color:#91dbc0;background:#203d36;border:1px solid #396455}.confirmation svg{width:30px;height:30px}
.status{color:#91dbc0;font-size:13px;margin:0 0 12px}h1{font-size:26px;font-weight:600;line-height:1.4;margin:0 0 14px}.description{color:#a4b2c7;font-size:15px;line-height:1.9;margin:0 auto;max-width:350px}
.steps{margin:28px 0 24px;padding:0;list-style:none;text-align:left;display:grid;gap:10px}.steps li{display:flex;align-items:center;gap:12px;padding:14px 16px;border:1px solid #30384b;border-radius:12px;background:#192231}.number{display:grid;place-items:center;width:26px;height:26px;flex-shrink:0;border-radius:50%;background:#2b2c47;color:#c6bbff;font-size:12px}.steps strong{font-size:14px;font-weight:500}.steps small{margin-left:auto;color:#8b9db5;font-size:12px}.steps li:first-child .number{background:#203d36;color:#91dbc0}
.footer{font-size:13px;line-height:1.8;color:#8698b1;margin:0}
@media(max-width:480px){body{padding:20px 16px}main{padding:30px 20px 26px;border-radius:18px}h1{font-size:23px}.brand{margin-bottom:26px}.steps li{padding:12px}.steps small{font-size:11px}}
`

const loginCallbackHTML = `<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>HQIP · 授权已送达</title><style>` + loginCallbackStyle + `</style></head>
<body><main aria-labelledby="title">
<div class="brand"><svg viewBox="0 0 64 64" aria-hidden="true"><rect width="64" height="64" rx="18" fill="#8c80ff"/><path d="M16 18h8v12h18V18h8v30h-8V36H24v12h-8z" fill="#101722"/><circle cx="47" cy="18" r="6" fill="#9cf0d1"/></svg><div class="wordmark">HQIP<small>YOUR CONNECTION</small></div></div>
<div class="confirmation"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m5 12 4 4L19 6"/></svg></div>
<p class="status">登录授权已送达</p><h1 id="title">回到 HQIP，继续连接</h1>
<p class="description">请返回客户端查看登录结果。<br>确认登录后，即可选择订阅和节点。</p>
<ol class="steps"><li><span class="number">1</span><strong>浏览器授权</strong><small>已完成</small></li><li><span class="number">2</span><strong>返回 HQIP 客户端</strong><small>查看登录结果</small></li></ol>
<p class="footer">现在可以关闭此页面。<br>如客户端提示登录失败，请在客户端重新发起登录。</p>
</main></body></html>`
