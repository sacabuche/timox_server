package main

import (
	"context"
	"embed"
	"net/http"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed locales/*.toml
var localeFS embed.FS

var i18nBundle *i18n.Bundle

type ctxKey string

const localizerKey ctxKey = "localizer"
const langKey ctxKey = "lang"

var supportedLangs = []string{"en", "es", "fr"}

func init() {
	i18nBundle = i18n.NewBundle(language.English)
	i18nBundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	for _, lang := range supportedLangs {
		if _, err := i18nBundle.LoadMessageFileFS(localeFS, "locales/"+lang+".toml"); err != nil {
			panic(err)
		}
	}
}

func languageMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Query param overrides (set cookie + redirect)
		if qLang := r.URL.Query().Get("lang"); qLang != "" && isSupportedLang(qLang) {
			http.SetCookie(w, &http.Cookie{Name: "lang", Value: qLang, Path: "/", MaxAge: 365 * 24 * 3600})
			// Redirect stripping the lang param
			q := r.URL.Query()
			q.Del("lang")
			r.URL.RawQuery = q.Encode()
			http.Redirect(w, r, r.URL.String(), http.StatusFound)
			return
		}
		// 2. Cookie
		lang := ""
		if c, err := r.Cookie("lang"); err == nil && isSupportedLang(c.Value) {
			lang = c.Value
		}
		// 3. Accept-Language header
		accept := r.Header.Get("Accept-Language")
		localizer := i18n.NewLocalizer(i18nBundle, lang, accept, "en")
		ctx := context.WithValue(r.Context(), localizerKey, localizer)
		ctx = context.WithValue(ctx, langKey, activeLang(lang, accept))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isSupportedLang(lang string) bool {
	for _, l := range supportedLangs {
		if l == lang {
			return true
		}
	}
	return false
}

func activeLang(cookie, accept string) string {
	if cookie != "" {
		return cookie
	}
	// parse first preferred from Accept-Language
	for _, part := range strings.Split(accept, ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		base := strings.SplitN(tag, "-", 2)[0]
		if isSupportedLang(base) {
			return base
		}
	}
	return "en"
}

func getLocalizer(r *http.Request) *i18n.Localizer {
	if l, ok := r.Context().Value(localizerKey).(*i18n.Localizer); ok {
		return l
	}
	return i18n.NewLocalizer(i18nBundle, "en")
}

func getLang(r *http.Request) string {
	if l, ok := r.Context().Value(langKey).(string); ok {
		return l
	}
	return "en"
}

func makeT(localizer *i18n.Localizer) func(string, ...interface{}) string {
	return func(key string, args ...interface{}) string {
		cfg := &i18n.LocalizeConfig{MessageID: key}
		if len(args) > 0 {
			cfg.TemplateData = args[0]
		}
		s, err := localizer.Localize(cfg)
		if err != nil {
			return key
		}
		return s
	}
}
