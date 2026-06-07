package render

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
)

// Result is a ready HTTP response from the engine.
type Result struct {
	Status      int
	ContentType string
	Location    string // for http_redirect
	Body        []byte
}

// Options holds the render parameters.
type Options struct {
	ContentType string // Content-Type override (from the group/stream config)
	Path        string // for under_construction (the image)
	// fields for api/show_out type responses
	Country  string
	Region   string
	City     string
	Device   string
	Operator string
	Bot      string
	Uniq     string
	Lang     string
	Mac      string
}

// Do builds a Result from the redirect type and the already-expanded out.
// The api/show_out types are returned as JSON.
func Do(redirect, out string, o Options) Result {
	switch redirect {
	case "http_redirect":
		return Result{Status: http.StatusFound, Location: out}

	case "javascript", "js_selection_raw":
		return Result{Status: 200, ContentType: ct(o, "application/javascript; charset=utf-8"), Body: []byte(out)}

	case "show_text":
		return Result{Status: 200, ContentType: ct(o, "text/plain; charset=utf-8"), Body: []byte(out)}

	case "meta_refresh":
		return htmlResult(200, fmt.Sprintf(`<!DOCTYPE html><head><meta http-equiv="refresh" content="0; URL=%s"></head><body></body></html>`, attr(out)))

	case "js_redirect":
		return htmlResult(200, fmt.Sprintf(`<!DOCTYPE html><head><meta http-equiv="refresh" content="1; URL=%s">`+
			`<script>window.location=%s;</script></head><body>The Document has moved <a href="%s">here</a></body></html>`,
			attr(out), jsStr(out), attr(out)))

	case "iframe_redirect":
		return htmlResult(200, fmt.Sprintf(`<!DOCTYPE html><head><meta http-equiv="content-type" content="text/html; charset=utf-8"></head>`+
			`<body><iframe src="javascript:parent.location=%s" style="visibility:hidden"></iframe>`+
			`<script>function go(){location.replace(%s)} window.setTimeout(go,1000)</script></body></html>`,
			jsAttr(out), jsStr(out)))

	case "js_selection":
		return Result{Status: 200, ContentType: ct(o, "application/javascript; charset=utf-8"),
			Body: []byte(fmt.Sprintf("function process(){window.location=%s;}\nwindow.onerror=process;\nprocess()", jsStr(out)))}

	case "iframe_selection":
		return htmlResult(200, fmt.Sprintf(`<script type="text/javascript">function process(){top.location=%s;}`+
			`window.onerror=process;if(top.location.href!=window.location.href){process()}</script>`, jsStr(out)))

	case "show_page_html":
		return htmlResult(200, `<!DOCTYPE html><head><meta name="robots" content="noindex,nofollow">`+
			`<meta http-equiv="content-type" content="text/html; charset=utf-8"></head><body>`+out+`</body></html>`)

	case "under_construction":
		return htmlResult(200, fmt.Sprintf(`<!DOCTYPE html><head><meta http-equiv="content-type" content="text/html; charset=utf-8">`+
			`<meta name="robots" content="noindex, nofollow"><title>Under construction</title></head>`+
			`<body><br><center><img src="http://%s/files/img/404.png" border=0></center></body></html>`, attr(o.Path)))

	case "403_forbidden":
		return htmlResult(http.StatusForbidden, `<!DOCTYPE html><head><title>Access forbidden!</title></head><body><h1>Access forbidden!</h1><h2>Error 403</h2></body></html>`)

	case "404_not_found":
		return htmlResult(http.StatusNotFound, `<!DOCTYPE html><head><title>Object not found!</title></head><body><h1>Object not found!</h1><h2>Error 404</h2></body></html>`)

	case "500_server_error":
		return htmlResult(http.StatusInternalServerError, `<!DOCTYPE html><head><title>Server error!</title></head><body><h1>Server error!</h1><h2>Error 500</h2></body></html>`)

	case "api":
		b, _ := json.Marshal(map[string]any{
			"out": out, "type": 0, "country": o.Country, "region": o.Region,
			"city": o.City, "device": o.Device, "operator": o.Operator,
			"bot": o.Bot, "uniq": o.Uniq, "lang": o.Lang, "mac": o.Mac,
		})
		return Result{Status: 200, ContentType: "application/json; charset=utf-8", Body: b}

	case "show_out":
		b, _ := json.Marshal(map[string]any{"out": out, "type": 1, "mac": o.Mac})
		return Result{Status: 200, ContentType: "application/json; charset=utf-8", Body: b}

	case "stop", "":
		return Result{Status: 200}

	default:
		return Result{Status: 200}
	}
}

// Write writes the Result to the http.ResponseWriter.
func (r Result) Write(w http.ResponseWriter) {
	if r.Location != "" {
		w.Header().Set("Location", r.Location)
		w.WriteHeader(r.Status)
		return
	}
	if r.ContentType != "" {
		w.Header().Set("Content-Type", r.ContentType)
	}
	w.WriteHeader(r.Status)
	if len(r.Body) > 0 {
		_, _ = w.Write(r.Body)
	}
}

func htmlResult(status int, body string) Result {
	return Result{Status: status, ContentType: "text/html; charset=utf-8", Body: []byte(body)}
}

func ct(o Options, def string) string {
	if o.ContentType != "" {
		return o.ContentType
	}
	return def
}

// attr escapes a value for insertion into an HTML attribute/text.
func attr(s string) string { return html.EscapeString(s) }

// jsStr returns a safe JS string literal ("...") for insertion into <script>.
// json.Marshal escapes < > & (which also prevents breaking out via </script>).
func jsStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// jsAttr is a JS string, additionally escaped for an HTML attribute.
func jsAttr(s string) string { return html.EscapeString(jsStr(s)) }
