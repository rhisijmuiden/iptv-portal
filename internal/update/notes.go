package update

import (
	"html"
	"html/template"
	"regexp"
	"strings"
)

var (
	linkRE = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	urlRE  = regexp.MustCompile(`(^|[\s(])(https?://[^\s<)]+)`)
	boldRE = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	codeRE = regexp.MustCompile("`([^`]+)`")
	prRE   = regexp.MustCompile(`https://github\.com/[^/\s]+/[^/\s]+/pull/(\d+)`)
)

// RenderNotes turns release notes written in the small Markdown subset
// GitHub generates (headings, bullet lists, links, bold, code) into HTML.
// Everything else is shown as escaped text.
func RenderNotes(md string) template.HTML {
	var b strings.Builder
	inList := false
	closeList := func() {
		if inList {
			b.WriteString("</ul>\n")
			inList = false
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "" || strings.HasPrefix(t, "<!--"):
			closeList()
		case strings.HasPrefix(t, "#"):
			closeList()
			level := len(t) - len(strings.TrimLeft(t, "#"))
			text := strings.TrimSpace(t[level:])
			tag := "h4"
			if level <= 2 {
				tag = "h3"
			}
			b.WriteString("<" + tag + ">" + inline(text) + "</" + tag + ">\n")
		case strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "- "):
			if !inList {
				b.WriteString("<ul>\n")
				inList = true
			}
			b.WriteString("<li>" + inline(t[2:]) + "</li>\n")
		default:
			closeList()
			b.WriteString("<p>" + inline(t) + "</p>\n")
		}
	}
	closeList()
	return template.HTML(b.String())
}

// inline escapes text and then adds links, bold and code. Escaping first
// means no HTML from the notes is ever passed through.
func inline(s string) string {
	s = html.EscapeString(s)
	s = linkRE.ReplaceAllStringFunc(s, func(m string) string {
		p := linkRE.FindStringSubmatch(m)
		return `<a href="` + p[2] + `" target="_blank" rel="noopener">` + p[1] + `</a>`
	})
	s = urlRE.ReplaceAllStringFunc(s, func(m string) string {
		p := urlRE.FindStringSubmatch(m)
		label := p[2]
		if pr := prRE.FindStringSubmatch(p[2]); pr != nil {
			label = "#" + pr[1]
		}
		return p[1] + `<a href="` + p[2] + `" target="_blank" rel="noopener">` + label + `</a>`
	})
	s = boldRE.ReplaceAllString(s, "<strong>$1</strong>")
	s = codeRE.ReplaceAllString(s, "<code>$1</code>")
	return s
}
