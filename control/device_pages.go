package control

import (
	"html/template"
	"net/http"
)

// devicePageContent is one static Device Flow page. Detail is untrusted text
// from Hydra's error redirect and is only ever HTML-escaped.
type devicePageContent struct {
	Title, Message, Detail string
}

var (
	pageInvalid     = devicePageContent{Title: "链接无效", Message: "这个设备授权链接不完整或已失效。请重新运行 agentd login。"}
	pageExpired     = devicePageContent{Title: "授权已过期", Message: "这次设备授权已过期或已使用。请重新运行 agentd login。"}
	pageConflict    = devicePageContent{Title: "无法继续", Message: "这次设备授权属于另一个浏览器会话。请重新运行 agentd login。"}
	pageUnavailable = devicePageContent{Title: "暂时不可用", Message: "授权服务暂时不可用，请稍后刷新页面重试。"}
	pageDone        = devicePageContent{Title: "设备已授权", Message: "可以关闭此页面并返回终端，agentd 会自动完成注册。"}
	pageDeclined    = devicePageContent{Title: "设备未授权", Message: "这次设备注册没有完成。请在终端按 Ctrl+C 结束 agentd login，需要时重新运行。"}
)

// The shadcn/ui neutral theme tokens, so these pages match the Console.
const devicePageStyle = `<style>
:root{color-scheme:light dark;--background:oklch(0.97 0 0);--card:oklch(1 0 0);--foreground:oklch(0.145 0 0);--muted-foreground:oklch(0.556 0 0);--border:oklch(0.922 0 0);--primary:oklch(0.205 0 0);--primary-foreground:oklch(0.985 0 0);--destructive:oklch(0.577 0.245 27.325);--ring:oklch(0.708 0 0);font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}
@media (prefers-color-scheme:dark){:root{--background:oklch(0.145 0 0);--card:oklch(0.205 0 0);--foreground:oklch(0.985 0 0);--muted-foreground:oklch(0.708 0 0);--border:oklch(1 0 0 / 10%);--primary:oklch(0.922 0 0);--primary-foreground:oklch(0.205 0 0);--destructive:oklch(0.704 0.191 22.216);--ring:oklch(0.556 0 0)}}
body{min-height:100svh;margin:0;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:1.5rem;padding:1.5rem;box-sizing:border-box;background:var(--background);color:var(--foreground);font-size:.875rem}
.brand{display:flex;align-items:center;gap:.5rem;font-weight:500}.brand span{display:flex;width:1.5rem;height:1.5rem;align-items:center;justify-content:center;border-radius:.375rem;background:var(--primary);color:var(--primary-foreground)}
main{width:min(24rem,100%);padding:1.5rem;border:1px solid var(--border);border-radius:.875rem;background:var(--card);box-sizing:border-box}
h1{margin:0 0 .375rem;font-size:1.25rem;font-weight:600;letter-spacing:-.01em}p{margin:0;line-height:1.6;color:var(--muted-foreground)}
label{display:block;margin:1.25rem 0 .5rem;font-weight:500}
input,button{width:100%;box-sizing:border-box;height:2.25rem;padding:0 .75rem;border-radius:.625rem;font:inherit}
input{border:1px solid var(--border);background:transparent;color:var(--foreground);font-family:ui-monospace,monospace;letter-spacing:.08em;outline:none}input:focus{border-color:var(--ring);box-shadow:0 0 0 3px color-mix(in oklch,var(--ring) 50%,transparent)}
button{margin-top:1rem;border:0;background:var(--primary);color:var(--primary-foreground);font-weight:500;cursor:pointer}button:hover{opacity:.9}
.error{margin-top:1rem;padding:.75rem;border:1px solid color-mix(in oklch,var(--destructive) 30%,transparent);border-radius:.625rem;color:var(--destructive)}
.detail{margin-top:.75rem;font-size:.8125rem;word-break:break-word}
</style>`

var devicePageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · agents</title>` + devicePageStyle + `</head>
<body><div class="brand"><span>a</span>agents</div><main><h1>{{.Title}}</h1><p>{{.Message}}</p>{{if .Detail}}<p class="detail">{{.Detail}}</p>{{end}}</main></body></html>`))

var deviceCodeTemplate = template.Must(template.New("code").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>注册设备 · agents</title>` + devicePageStyle + `</head>
<body><div class="brand"><span>a</span>agents</div><main>
<h1>注册 agentd 设备</h1>
<p>输入终端中 agentd login 显示的验证码。只在你自己刚刚运行了 agentd login 时继续。</p>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<form method="post" action="/oauth/device">
<input type="hidden" name="device_challenge" value="{{.Challenge}}">
<label for="user_code">验证码</label>
<input id="user_code" name="user_code" value="{{.UserCode}}" autocomplete="one-time-code" autocapitalize="off" spellcheck="false" required maxlength="64" autofocus>
<button type="submit">继续</button>
</form>
</main></body></html>`))

// Pages carry no script. form-action is omitted on purpose: browsers apply it
// to the whole redirect chain, and the form must continue to Hydra. With the
// global no-referrer policy browsers send "Origin: null" on the form post;
// same-origin keeps the real Origin for Control and none for Hydra.
func devicePageHeaders(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
}

func (a *App) devicePage(w http.ResponseWriter, status int, page devicePageContent) {
	devicePageHeaders(w, status)
	_ = devicePageTemplate.Execute(w, page)
}

func (a *App) deviceCodeForm(w http.ResponseWriter, status int, challenge, userCode, message string) {
	devicePageHeaders(w, status)
	_ = deviceCodeTemplate.Execute(w, struct{ Challenge, UserCode, Error string }{challenge, userCode, message})
}
