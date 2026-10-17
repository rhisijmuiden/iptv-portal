package epg

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sample(now time.Time) string {
	f := func(t time.Time) string { return t.Format("20060102150405 -0700") }
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="nh.nl"><display-name>NH</display-name></channel>
  <channel id="other"><display-name>Other</display-name></channel>
  <programme start="%s" stop="%s" channel="nh.nl"><title lang="nl">Nieuws &amp; weer</title><desc>Het laatste nieuws</desc></programme>
  <programme start="%s" stop="%s" channel="nh.nl"><title>Sport</title></programme>
  <programme start="%s" stop="%s" channel="nh.nl"><title>Oud</title></programme>
  <programme start="%s" stop="%s" channel="other"><title>X</title></programme>
</tv>`,
		f(now.Add(-30*time.Minute)), f(now.Add(30*time.Minute)),
		f(now.Add(30*time.Minute)), f(now.Add(90*time.Minute)),
		f(now.Add(-48*time.Hour)), f(now.Add(-47*time.Hour)),
		f(now), f(now.Add(time.Hour)))
}

func TestRefreshAndWrite(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	io.WriteString(zw, sample(now))
	zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/plain.xml":
			io.WriteString(w, sample(now))
		case "/guide.xml.gz":
			w.Write(gz.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	g := New(slog.New(slog.DiscardHandler), dir)
	if err := g.Refresh(context.Background(), []string{srv.URL + "/plain.xml", srv.URL + "/guide.xml.gz", srv.URL + "/missing"}); err != nil {
		t.Fatal(err)
	}
	st := g.Status()
	if st.Channels != 2 || st.Programmes != 3 || st.Sources[2].Error == "" || st.Sources[1].Programmes != 3 {
		t.Fatalf("status = %+v", st)
	}
	ps := g.Programmes("nh.nl", now, now.Add(24*time.Hour))
	if len(ps) != 2 || ps[0].Title != "Nieuws & weer" || ps[0].Desc != "Het laatste nieuws" {
		t.Fatalf("programmes = %+v", ps)
	}

	var out bytes.Buffer
	if err := g.WriteXMLTV(&out, []Channel{{ID: "nh.nl", Name: "NH <HD>"}, {ID: ""}}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, `<channel id="nh.nl"><display-name>NH &lt;HD&gt;</display-name>`) ||
		strings.Count(s, "<programme") != 2 || strings.Contains(s, `channel="other"`) {
		t.Fatalf("xmltv = %s", s)
	}

	// All sources failing keeps the previous guide.
	if err := g.Refresh(context.Background(), []string{srv.URL + "/missing"}); err == nil {
		t.Fatal("expected error")
	}
	if !g.Has("nh.nl") {
		t.Fatal("guide lost after failed refresh")
	}

	// The cache is loaded on start.
	g2 := New(slog.New(slog.DiscardHandler), dir)
	if got := g2.Programmes("nh.nl", now, now.Add(24*time.Hour)); len(got) != 2 {
		t.Fatalf("cached programmes = %+v", got)
	}
}

func TestParseTime(t *testing.T) {
	for in, want := range map[string]string{
		"20261017120000 +0200": "2026-10-17T10:00:00Z",
		"20261017120000+0200":  "2026-10-17T10:00:00Z",
		"20261017120000":       "2026-10-17T12:00:00Z",
	} {
		got, err := ParseTime(in)
		if err != nil || got.UTC().Format(time.RFC3339) != want {
			t.Errorf("ParseTime(%q) = %v, %v", in, got, err)
		}
	}
}

func TestLatin1(t *testing.T) {
	doc := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><tv><programme start=\"20991017120000\" channel=\"c\"><title>Caf\xe9</title></programme></tv>"
	// Use a current timestamp so the retention window keeps the programme.
	progs, names := map[string][]Programme{}, map[string]string{}
	doc = strings.Replace(doc, "20991017120000", time.Now().UTC().Format("20060102150405"), 1)
	if _, _, err := parse(strings.NewReader(doc), progs, names); err != nil {
		t.Fatal(err)
	}
	if got := progs["c"][0].Title; got != "Café" {
		t.Fatalf("title = %q", got)
	}
}
