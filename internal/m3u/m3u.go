// Package m3u parses extended M3U playlists (#EXTM3U / #EXTINF) as used by
// IPTV players.
package m3u

import (
	"bufio"
	"io"
	"strings"
)

// Entry is one stream from a playlist.
type Entry struct {
	Name  string
	URL   string
	Attrs map[string]string // tvg-id, tvg-logo, group-title, ...
}

// Attr returns an attribute value or "".
func (e Entry) Attr(key string) string { return e.Attrs[key] }

// Playlist is a parsed playlist.
type Playlist struct {
	Attrs   map[string]string // attributes of the #EXTM3U header line
	Entries []Entry
}

// EPGURLs returns the guide URLs announced in the header (x-tvg-url or
// url-tvg; several URLs may be separated by commas).
func (p Playlist) EPGURLs() []string {
	var out []string
	for _, key := range []string{"x-tvg-url", "url-tvg"} {
		for _, u := range strings.Split(p.Attrs[key], ",") {
			if u = strings.TrimSpace(u); u != "" {
				out = append(out, u)
			}
		}
	}
	return out
}

// Parse reads a playlist. Lines it does not understand are ignored, so a
// plain list of URLs is accepted too (the URL is then used as name).
func Parse(r io.Reader) (Playlist, error) {
	pl := Playlist{Attrs: map[string]string{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var pending *Entry
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if first {
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXTM3U"):
			pl.Attrs = parseExtinf("#EXTINF:" + strings.TrimPrefix(line, "#EXTM3U")).Attrs
		case strings.HasPrefix(line, "#EXTINF"):
			e := parseExtinf(line)
			pending = &e
		case strings.HasPrefix(line, "#EXTGRP:"):
			if pending != nil && pending.Attrs["group-title"] == "" {
				pending.Attrs["group-title"] = strings.TrimSpace(strings.TrimPrefix(line, "#EXTGRP:"))
			}
		case strings.HasPrefix(line, "#"):
			// #EXTM3U, #EXTVLCOPT and other directives are not needed.
		default:
			e := Entry{Name: line, Attrs: map[string]string{}}
			if pending != nil {
				e = *pending
				pending = nil
			}
			e.URL = line
			if e.Name == "" {
				e.Name = line
			}
			pl.Entries = append(pl.Entries, e)
		}
	}
	return pl, sc.Err()
}

// parseExtinf parses `#EXTINF:-1 tvg-id="x" group-title="y",Display name`.
func parseExtinf(line string) Entry {
	e := Entry{Attrs: map[string]string{}}
	rest := strings.TrimPrefix(line, "#EXTINF:")
	// The display name follows the first comma that is outside quotes.
	inQuote := false
	split := -1
	for i, r := range rest {
		if r == '"' {
			inQuote = !inQuote
		} else if r == ',' && !inQuote {
			split = i
			break
		}
	}
	head := rest
	if split >= 0 {
		head = rest[:split]
		e.Name = strings.TrimSpace(rest[split+1:])
	}
	// head is "<duration> key="value" key2="value2"".
	for {
		eq := strings.Index(head, `="`)
		if eq < 0 {
			break
		}
		keyStart := strings.LastIndexAny(head[:eq], " \t") + 1
		key := strings.ToLower(strings.TrimSpace(head[keyStart:eq]))
		valEnd := strings.Index(head[eq+2:], `"`)
		if valEnd < 0 {
			break
		}
		if key != "" {
			e.Attrs[key] = strings.TrimSpace(head[eq+2 : eq+2+valEnd])
		}
		head = head[eq+2+valEnd+1:]
	}
	return e
}
