// Package guides turns the setup guides of the account pages into HTML.
//
// A guide is written in a small subset of Markdown, so an admin can edit
// it without knowing HTML:
//
//	## Heading
//	1. A numbered step
//	- A bullet
//	> A tip, shown highlighted
//	**bold**, `code` and [a link](https://example.com)
//
// Lines that follow each other form one paragraph; an empty line starts a
// new one. Placeholders such as {server} are replaced with the details of
// the user reading the guide (see Vars).
package guides

import (
	"html"
	"html/template"
	"regexp"
	"strings"
)

// Vars are the values filled in for the placeholders of a guide.
type Vars struct {
	Portal   string // {portal}: the name of the portal
	Server   string // {server}: the address entered in the player
	Username string // {username}
	M3U      string // {m3u}: the M3U playlist link, with a password placeholder
	EPG      string // {epg}: the XMLTV guide link, with a password placeholder
}

// Placeholders lists the placeholders a guide can use, for the help text
// of the admin form.
var Placeholders = []string{"{portal}", "{server}", "{username}", "{m3u}", "{epg}"}

var (
	codeRE = regexp.MustCompile("`([^`]+)`")
	boldRE = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	linkRE = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^\s)]+)\)`)
	listRE = regexp.MustCompile(`^\d+[.)]\s+`)
)

// inline formats one line of escaped text.
func inline(s string, v Vars) string {
	s = html.EscapeString(s)
	s = codeRE.ReplaceAllString(s, "<code>$1</code>")
	s = boldRE.ReplaceAllString(s, "<strong>$1</strong>")
	s = linkRE.ReplaceAllString(s, `<a href="$2" target="_blank" rel="noopener">$1</a>`)
	return strings.NewReplacer(
		"{portal}", html.EscapeString(v.Portal),
		"{server}", html.EscapeString(v.Server),
		"{username}", html.EscapeString(v.Username),
		"{m3u}", html.EscapeString(v.M3U),
		"{epg}", html.EscapeString(v.EPG),
	).Replace(s)
}

// Render turns the text of a guide into HTML. Everything the text contains
// is escaped, so a guide cannot add scripts or other markup to the page.
func Render(text string, v Vars) template.HTML {
	var b strings.Builder
	open := "" // the block being written: "p", "ol", "ul" or "tip"
	closeBlock := func() {
		switch open {
		case "p":
			b.WriteString("</p>\n")
		case "ol":
			b.WriteString("</ol>\n")
		case "ul":
			b.WriteString("</ul>\n")
		case "tip":
			b.WriteString("</p>\n")
		}
		open = ""
	}
	start := func(block, tag string) {
		if open == block {
			return
		}
		closeBlock()
		b.WriteString(tag)
		open = block
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			closeBlock()
		case strings.HasPrefix(line, "#"):
			closeBlock()
			b.WriteString("<h3>" + inline(strings.TrimSpace(strings.TrimLeft(line, "#")), v) + "</h3>\n")
		case listRE.MatchString(line):
			start("ol", "<ol class=\"steps\">\n")
			b.WriteString("<li>" + inline(listRE.ReplaceAllString(line, ""), v) + "</li>\n")
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			start("ul", "<ul>\n")
			b.WriteString("<li>" + inline(line[2:], v) + "</li>\n")
		case strings.HasPrefix(line, ">"):
			if open == "tip" {
				b.WriteString(" ")
			}
			start("tip", `<p class="tip">`)
			b.WriteString(inline(strings.TrimSpace(line[1:]), v))
		default:
			if open == "p" {
				b.WriteString(" ")
			}
			start("p", "<p>")
			b.WriteString(inline(line, v))
		}
	}
	closeBlock()
	return template.HTML(b.String())
}

// Summary is the first paragraph of a guide as plain text, for the list
// of guides.
func Summary(text string) string {
	var parts []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(parts) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "#") || listRE.MatchString(line) || strings.HasPrefix(line, "- ") || strings.HasPrefix(line, ">") {
			if len(parts) > 0 {
				break
			}
			continue
		}
		parts = append(parts, line)
	}
	s := strings.Join(parts, " ")
	s = codeRE.ReplaceAllString(s, "$1")
	s = boldRE.ReplaceAllString(s, "$1")
	s = linkRE.ReplaceAllString(s, "$1")
	if r := []rune(s); len(r) > 160 {
		s = strings.TrimSpace(string(r[:157])) + "…"
	}
	return s
}
