package main

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"time"
)

// --- Templates ---

//go:embed templates/*.html
var templateFiles embed.FS

var funcMap = template.FuncMap{
	"today": func() string { return time.Now().Format("2006-01-02") },
	"sub":   func(a, b int) int { return a - b },
	"fmtMin": func(m int) string {
		if m < 60 {
			return fmt.Sprintf("%d", m)
		}
		return fmt.Sprintf("%dh%02d", m/60, m%60)
	},
	"dict": func(pairs ...interface{}) map[string]interface{} {
		m := make(map[string]interface{}, len(pairs)/2)
		for i := 0; i+1 < len(pairs); i += 2 {
			key, ok := pairs[i].(string)
			if !ok {
				continue
			}
			m[key] = pairs[i+1]
		}
		return m
	},
}

var tmpl = template.Must(
	template.New("").Funcs(funcMap).ParseFS(templateFiles, "templates/*.html"),
)

func renderPage(w http.ResponseWriter, r *http.Request, contentTemplate string, data map[string]interface{}) {
	if data == nil {
		data = map[string]interface{}{}
	}
	t := makeT(getLocalizer(r))
	data["T"] = t
	var content bytes.Buffer
	if err := tmpl.ExecuteTemplate(&content, contentTemplate, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, "layout", map[string]interface{}{
		"Content": template.HTML(content.String()),
		"Lang":    getLang(r),
		"T":       t,
	})
}

func renderPartial(w http.ResponseWriter, r *http.Request, name string, data map[string]interface{}) {
	if data == nil {
		data = map[string]interface{}{}
	}
	data["T"] = makeT(getLocalizer(r))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, name, data)
}
