// Package i18n translates the user interface. English is the source
// language: every text in the templates and the Go code is written in
// English and used as the key into the catalogs of the other languages
// (nl.json for Dutch). A text without a translation is shown in English.
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
)

// Language codes.
const (
	English = "en"
	Dutch   = "nl"
	// Default is the language used when nothing else is chosen.
	Default = English
)

// Cookie is the cookie that holds the language a visitor picked.
const Cookie = "lang"

// Language is one selectable language.
type Language struct {
	Code string // e.g. "nl"
	Name string // the name in the language itself, e.g. "Nederlands"
}

// Languages are the selectable languages, English first.
var Languages = []Language{{English, "English"}, {Dutch, "Nederlands"}}

//go:embed nl.json
var catalogFS embed.FS

// catalogs maps a language code to its translations, keyed by the English
// text. English has no catalog.
var catalogs = map[string]map[string]string{}

func init() {
	b, err := catalogFS.ReadFile("nl.json")
	if err != nil {
		panic(err)
	}
	nl := map[string]string{}
	if err := json.Unmarshal(b, &nl); err != nil {
		panic("i18n: nl.json: " + err.Error())
	}
	catalogs[Dutch] = nl
}

// Match returns the supported language code for code ("nl", "nl-BE" and
// "NL" all give "nl"), or "" when it is not supported.
func Match(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(code, "-_"); i >= 0 {
		code = code[:i]
	}
	for _, l := range Languages {
		if l.Code == code {
			return code
		}
	}
	return ""
}

// T returns the translation of s, or s itself when there is none.
func T(lang, s string) string {
	if t, ok := catalogs[lang][s]; ok && t != "" {
		return t
	}
	return s
}

// Tf translates format and fills in args like fmt.Sprintf.
func Tf(lang, format string, args ...any) string {
	return fmt.Sprintf(T(lang, format), args...)
}

// HTML translates format, which may contain HTML of our own, and fills in
// args HTML-escaped. Use it for sentences with a link in the middle.
func HTML(lang, format string, args ...any) template.HTML {
	esc := make([]any, len(args))
	for i, a := range args {
		switch a.(type) {
		case int, int64, float64, bool:
			esc[i] = a // safe as is, and keeps %d working
		default:
			esc[i] = template.HTMLEscapeString(fmt.Sprint(a))
		}
	}
	return template.HTML(fmt.Sprintf(T(lang, format), esc...))
}

var dutchMonths = [...]string{"januari", "februari", "maart", "april", "mei", "juni", "juli",
	"augustus", "september", "oktober", "november", "december"}

// LongDate writes a date out in full, e.g. "2 January 2006" or
// "2 januari 2006".
func LongDate(lang string, t time.Time) string {
	if lang == Dutch {
		return fmt.Sprintf("%d %s %d", t.Day(), dutchMonths[t.Month()-1], t.Year())
	}
	return t.Format("2 January 2006")
}

// Ago describes how long ago t was, relative to now; older than a day
// gives the date and time.
func Ago(lang string, now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return T(lang, "just now")
	case d < time.Hour:
		return Tf(lang, "%d min ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return Tf(lang, "%d h ago", int(d/time.Hour))
	}
	return t.Local().Format("2006-01-02 15:04")
}

// FromRequest returns the language the visitor picked with the language
// switch, or fallback when they did not pick one.
func FromRequest(r *http.Request, fallback string) string {
	if c, err := r.Cookie(Cookie); err == nil {
		if l := Match(c.Value); l != "" {
			return l
		}
	}
	if l := Match(fallback); l != "" {
		return l
	}
	return Default
}

// Remember stores the picked language in a cookie for a year.
func Remember(w http.ResponseWriter, lang, path string, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: Cookie, Value: lang, Path: path, MaxAge: 365 * 24 * 3600,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

type ctxKey struct{}

// WithLanguage returns a context that carries lang.
func WithLanguage(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, ctxKey{}, lang)
}

// FromContext returns the language stored by WithLanguage, or Default.
func FromContext(ctx context.Context) string {
	if l, ok := ctx.Value(ctxKey{}).(string); ok {
		return l
	}
	return Default
}

// Funcs are the template functions for lang: t (translate), tf (translate
// and format), th (translate HTML), lang (the language code), languages,
// longdate and ago.
func Funcs(lang string) template.FuncMap {
	return template.FuncMap{
		"t":         func(s string) string { return T(lang, s) },
		"tf":        func(format string, args ...any) string { return Tf(lang, format, args...) },
		"th":        func(format string, args ...any) template.HTML { return HTML(lang, format, args...) },
		"lang":      func() string { return lang },
		"languages": func() []Language { return Languages },
		"longdate":  func(t time.Time) string { return LongDate(lang, t) },
		"ago":       func(now, t time.Time) string { return Ago(lang, now, t) },
	}
}
