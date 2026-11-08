package guides

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	v := Vars{Portal: "TV", Server: "http://tv.example", Username: "jan<b>", M3U: "http://tv.example/get.php?a=1&b=2"}
	got := string(Render("Intro line\nsecond line\n\n## Steps\n1. Open **app**\n2) Enter `{server}`\n- user {username}\n> tip\n\n[site](https://example.com) [bad](javascript:alert(1)) <script>x</script> {m3u}", v))
	for _, want := range []string{
		"<p>Intro line second line</p>",
		"<h3>Steps</h3>",
		`<ol class="steps">`, "<li>Open <strong>app</strong></li>", "<li>Enter <code>http://tv.example</code></li>",
		"<ul>\n<li>user jan&lt;b&gt;</li>", `<p class="tip">tip</p>`,
		`<a href="https://example.com" target="_blank" rel="noopener">site</a>`,
		"[bad](javascript:alert(1))", "&lt;script&gt;", "get.php?a=1&amp;b=2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>") || strings.Contains(got, `href="javascript`) {
		t.Errorf("unsafe markup:\n%s", got)
	}
}

func TestSummary(t *testing.T) {
	if got := Summary("\nFor **Formuler** boxes.\nMore.\n\n1. step"); got != "For Formuler boxes. More." {
		t.Errorf("Summary = %q", got)
	}
}

func TestDefaults(t *testing.T) {
	keys := map[string]bool{}
	for _, d := range Defaults {
		if d.Key == "" || d.Name == "" || d.Body == "" || d.NameNL == "" || d.BodyNL == "" || keys[d.Key] {
			t.Errorf("incomplete or duplicate default %q", d.Key)
		}
		keys[d.Key] = true
		if Summary(d.Body) == "" || strings.Contains(Summary(d.Body), "{") {
			t.Errorf("%s: summary %q", d.Key, Summary(d.Body))
		}
	}
}
