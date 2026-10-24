package i18n

import (
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// literal matches a Go or template string literal: "…" or `…`.
const literal = "(\"(?:[^\"\\\\]|\\\\.)*\"|`[^`]*`)"

var (
	// Texts passed to t, tf and th in the templates.
	templateText = regexp.MustCompile(`[{(](?:t|tf|th)\s+` + literal)
	// Texts translated in Go: tr(r, "…"), i18n.T(lang, "…"), t("…"), page
	// titles and flash messages.
	goText = regexp.MustCompile(`(?:\btr\(r,\s*|i18n\.Tf?\(\w+,\s*|\bt\(|Title:\s*|title :?= |redirectFlash\(w, r, [^,]+,\s*|redirectError\(w, r, [^,]+,\s*)` + literal)
	verb   = regexp.MustCompile(`%(?:\[\d+\])?[-+# 0-9.]*[a-zA-Z%]`)
)

func unquote(t *testing.T, lit string) string {
	t.Helper()
	if lit[0] == '`' {
		return lit[1 : len(lit)-1]
	}
	s, err := strconv.Unquote(lit)
	if err != nil {
		t.Fatalf("unquoting %s: %v", lit, err)
	}
	return s
}

// usedTexts collects the English texts of the templates and Go code under
// internal/, with the file each was found in.
func usedTexts(t *testing.T) map[string]string {
	t.Helper()
	used := map[string]string{}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "/i18n/") {
			return err
		}
		var re *regexp.Regexp
		switch {
		case strings.HasSuffix(path, ".html") && strings.Contains(path, "/templates/"):
			re = templateText
		case strings.HasSuffix(path, ".go"):
			re = goText
		default:
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if s := unquote(t, m[1]); s != "" && !strings.HasPrefix(s, "/") {
				used[s] = path
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return used
}

// TestDutchComplete fails when a text in the interface has no Dutch
// translation. Add the missing texts to nl.json.
func TestDutchComplete(t *testing.T) {
	used := usedTexts(t)
	if len(used) < 300 {
		t.Fatalf("found only %d texts, the scan is broken", len(used))
	}
	var missing []string
	for s, file := range used {
		if _, ok := catalogs[Dutch][s]; !ok {
			missing = append(missing, strconv.Quote(s)+" ("+file+")")
		}
	}
	slices.Sort(missing)
	for _, m := range missing {
		t.Errorf("no Dutch translation for %s", m)
	}
}

// TestDutchVerbs checks that every translation fills in the same values as
// the English text, so fmt never prints %!d(MISSING).
func TestDutchVerbs(t *testing.T) {
	for en, nl := range catalogs[Dutch] {
		a, b := verb.FindAllString(en, -1), verb.FindAllString(nl, -1)
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			t.Errorf("%q: verbs %v, translation %q has %v", en, a, nl, b)
		}
		if strings.TrimSpace(nl) == "" {
			t.Errorf("%q has an empty translation", en)
		}
	}
}

func TestTranslate(t *testing.T) {
	if got := T(Dutch, "Channels"); got != "Zenders" {
		t.Errorf("T(nl, Channels) = %q", got)
	}
	if got := T(English, "Channels"); got != "Channels" {
		t.Errorf("T(en, Channels) = %q", got)
	}
	if got := T(Dutch, "no such text"); got != "no such text" {
		t.Errorf("an unknown text gives %q", got)
	}
	if got := Tf(Dutch, "Version %s is available.", "v1.0.0"); got != "Versie v1.0.0 is beschikbaar." {
		t.Errorf("Tf = %q", got)
	}
	if got := string(HTML(English, "<b>%s</b> %d", "<x>", 3)); got != "<b>&lt;x&gt;</b> 3" {
		t.Errorf("HTML = %q", got)
	}
}

func TestMatch(t *testing.T) {
	for in, want := range map[string]string{"nl": "nl", "NL": "nl", "nl-BE": "nl", "en_GB": "en", "de": "", "": ""} {
		if got := Match(in); got != want {
			t.Errorf("Match(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFromRequest(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if got := FromRequest(r, ""); got != English {
		t.Errorf("no cookie, no default: %q", got)
	}
	if got := FromRequest(r, "nl"); got != Dutch {
		t.Errorf("no cookie, default nl: %q", got)
	}
	r.Header.Set("Cookie", Cookie+"=en")
	if got := FromRequest(r, "nl"); got != English {
		t.Errorf("cookie en, default nl: %q", got)
	}
	r.Header.Set("Cookie", Cookie+"=xx")
	if got := FromRequest(r, "nl"); got != Dutch {
		t.Errorf("bad cookie, default nl: %q", got)
	}
}

func TestDates(t *testing.T) {
	d := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	if got := LongDate(Dutch, d); got != "9 maart 2026" {
		t.Errorf("LongDate(nl) = %q", got)
	}
	if got := LongDate(English, d); got != "9 March 2026" {
		t.Errorf("LongDate(en) = %q", got)
	}
	if got := Ago(Dutch, d, d.Add(-5*time.Minute)); got != "5 min geleden" {
		t.Errorf("Ago(nl) = %q", got)
	}
}
