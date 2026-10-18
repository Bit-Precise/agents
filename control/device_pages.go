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

const devicePageStyle = `<style>
:root{color-scheme:light dark;font-family:system-ui,sans-serif}
body{min-height:100vh;margin:0;display:grid;place-items:center;background:#f8fafc;color:#172033}
main{width:min(30rem,calc(100% - 2rem));padding:2rem;border:1px solid #dbe2ea;border-radius:.75rem;background:#fff;box-sizing:border-box}
h1{margin:0 0 .75rem;font-size:1.4rem}p{line-height:1.6;color:#526074}
label{display:block;margin:1.25rem 0 .5rem;font-weight:600}
input,button{width:100%;box-sizing:border-box;padding:.7rem .8rem;border-radius:.5rem;font:inherit}
input{border:1px solid #b8c3d1;font-family:ui-monospace,monospace;letter-spacing:.08em}
button{margin-top:1rem;border:0;background:#183b32;color:#fff;font-weight:600;cursor:pointer}
.error{padding:.75rem;border-radius:.5rem;color:#991b1b;background:#fee2e2}
.detail{font-size:.9rem;word-break:break-word}
@media (prefers-color-scheme:dark){body{background:#07111f;color:#eef2ff}main{border-color:#263449;background:#101b2d}p{color:#aab7cb}input{border-color:#40506a;background:#0b1423;color:#eef2ff}}
</style>`

var devicePageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · agents</title>` + devicePageStyle + `</head>
<body><main><h1>{{.Title}}</h1><p>{{.Message}}</p>{{if .Detail}}<p class="detail">{{.Detail}}</p>{{end}}</main></body></html>`))

var deviceCodeTemplate = template.Must(template.New("code").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>注册设备 · agents</title>` + devicePageStyle + `</head>
<body><main>
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
