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
	"fmtMin": func(m int) string {
		if m < 60 {
			return fmt.Sprintf("%d", m)
		}
		return fmt.Sprintf("%dh%02d", m/60, m%60)
	},
}

var tmpl = template.Must(
	template.New("").Funcs(funcMap).ParseFS(templateFiles, "templates/*.html"),
)

func renderPage(w http.ResponseWriter, contentTemplate string, data interface{}) {
	var content bytes.Buffer
	if err := tmpl.ExecuteTemplate(&content, contentTemplate, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, "layout", map[string]interface{}{
		"Content": template.HTML(content.String()),
	})
}

func renderPartial(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, name, data)
}
