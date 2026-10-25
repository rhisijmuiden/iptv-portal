package portal

import (
	"bufio"
	"context"
	"io"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/i18n"
	"github.com/rhisijmuiden/iptv-portal/internal/mail"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// browser is an HTTP client with cookies that does not follow redirects.
type browser struct {
	t      *testing.T
	base   string
	client *http.Client
}

func newBrowser(t *testing.T, f fixture) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: f.srv.URL, client: &http.Client{Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) do(method, path string, form url.Values) (int, string, string) {
	b.t.Helper()
	req, _ := http.NewRequest(method, b.base+path, strings.NewReader(form.Encode()))
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(body)
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`)

func (b *browser) csrf(path string) string {
	b.t.Helper()
	_, _, body := b.do("GET", path, nil)
	m := csrfRE.FindStringSubmatch(body)
	if m == nil {
		b.t.Fatalf("no CSRF token on %s:\n%s", path, body)
	}
	return m[1]
}

func TestAccountPages(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := newBrowser(t, f)

	if code, loc, _ := b.do("GET", "/account", nil); code != http.StatusSeeOther || loc != "/account/login" {
		t.Fatalf("not logged in: %d %s", code, loc)
	}
	if code, _, body := b.do("POST", "/account/login", url.Values{"username": {"jan"}, "password": {"wrong1"}}); code != http.StatusUnauthorized ||
		!strings.Contains(body, "Invalid username or password.") {
		t.Fatalf("wrong password: %d\n%s", code, body)
	}
	if code, loc, _ := b.do("POST", "/account/login", url.Values{"username": {"jan"}, "password": {"secret1"}}); code != http.StatusSeeOther || loc != "/account" {
		t.Fatalf("login: %d %s", code, loc)
	}

	exp := time.Now().AddDate(0, 0, 10)
	u, _ := f.store.AuthenticateUser(ctx, "jan", "secret1")
	u.ExpiresAt = &exp
	if _, err := f.store.SaveUser(ctx, u, ""); err != nil {
		t.Fatal(err)
	}
	_, _, body := b.do("GET", "/account", nil)
	for _, want := range []string{"Hello, jan", "North", "Your account expires in 10 days", `<span class="badge on">Active</span>`, "Last used by your TV box"} {
		if !strings.Contains(body, want) {
			t.Errorf("overview lacks %q:\n%s", want, body)
		}
	}
	_, _, body = b.do("GET", "/account/setup", nil)
	if !strings.Contains(body, f.srv.URL+"/get.php?username=jan&amp;password=@") || strings.Contains(body, "secret1") {
		t.Errorf("setup page:\n%s", body)
	}

	// Viewing history shows only the user's own views.
	if code, _, _ := b.do("GET", "/live/jan/secret1/"+strconv.FormatInt(f.nh, 10)+".m3u8", nil); code != http.StatusFound {
		t.Fatalf("opening a channel: %d", code)
	}
	piet, err := f.store.SaveUser(ctx, store.User{Username: "piet", Enabled: true}, "secret1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.StartView(ctx, store.View{UserID: piet, Username: "piet", Channel: "Secret channel", Kind: store.KindTV,
		StartedAt: time.Now(), EndedAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	_, _, body = b.do("GET", "/account/history", nil)
	if !strings.Contains(body, "NH") || strings.Contains(body, "Secret channel") {
		t.Errorf("history:\n%s", body)
	}

	// Posts need the CSRF token.
	if code, _, _ := b.do("POST", "/account/email", url.Values{"email": {"jan@example.org"}}); code != http.StatusForbidden {
		t.Errorf("email without CSRF: %d", code)
	}
	csrf := b.csrf("/account/settings")
	if code, _, _ := b.do("POST", "/account/email", url.Values{"csrf": {csrf}, "email": {"jan@example.org"}}); code != http.StatusSeeOther {
		t.Errorf("saving email: %d", code)
	}
	if u, _ := f.store.User(ctx, u.ID); u.Email != "jan@example.org" {
		t.Errorf("email = %q", u.Email)
	}
	if code, _, body := b.do("POST", "/account/password", url.Values{"csrf": {csrf}, "current": {"secret1"}, "new": {"nieuw12"}, "confirm": {"anders1"}}); code != http.StatusBadRequest ||
		!strings.Contains(body, "The new passwords do not match.") {
		t.Errorf("mismatch: %d\n%s", code, body)
	}
	if code, _, body := b.do("POST", "/account/password", url.Values{"csrf": {csrf}, "current": {"wrong1"}, "new": {"nieuw12"}, "confirm": {"nieuw12"}}); code != http.StatusBadRequest ||
		!strings.Contains(body, "Current password is incorrect.") {
		t.Errorf("wrong current password: %d\n%s", code, body)
	}
	if code, loc, _ := b.do("POST", "/account/password", url.Values{"csrf": {csrf}, "current": {"secret1"}, "new": {"nieuw12"}, "confirm": {"nieuw12"}}); code != http.StatusSeeOther ||
		!strings.HasPrefix(loc, "/account/settings?ok=") {
		t.Errorf("changing password: %d %s", code, loc)
	}
	if _, err := f.store.AuthenticateUser(ctx, "jan", "nieuw12"); err != nil {
		t.Errorf("player login with the new password: %v", err)
	}
	if _, err := f.store.AuthenticateUser(ctx, "jan", "secret1"); err == nil {
		t.Error("the old password still works for the player")
	}

	if code, _, _ := b.do("POST", "/account/logout", url.Values{"csrf": {csrf}}); code != http.StatusSeeOther {
		t.Errorf("logout: %d", code)
	}
	if code, _, _ := b.do("GET", "/account", nil); code != http.StatusSeeOther {
		t.Errorf("still logged in after logout: %d", code)
	}
}

func TestAccountPagesOff(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := newBrowser(t, f)
	req, _ := http.NewRequest("GET", f.srv.URL+"/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/account" {
		t.Errorf("browser on /: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, _, body := b.do("GET", "/", nil); code != http.StatusOK || body != "IPTV Portal\n" {
		t.Errorf("player on /: %d %q", code, body)
	}

	st, _ := f.store.Settings(ctx)
	st.AccountPortal = false
	if err := f.store.SaveSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/account", "/account/login", "/account/forgot"} {
		if code, _, _ := b.do("GET", p, nil); code != http.StatusNotFound {
			t.Errorf("%s while off: %d", p, code)
		}
	}
}

// fakeSMTP accepts any number of mail sessions and passes on the decoded
// messages.
func fakeSMTP(t *testing.T) (mail.Config, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	msgs := make(chan string, 10)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				say := func(s string) { io.WriteString(c, s+"\r\n") }
				say("220 fake")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch strings.ToUpper(strings.SplitN(strings.TrimSpace(line), " ", 2)[0]) {
					case "EHLO":
						say("250 fake")
					case "DATA":
						say("354 go")
						var b strings.Builder
						for {
							l, err := r.ReadString('\n')
							if err != nil || l == ".\r\n" {
								break
							}
							b.WriteString(l)
						}
						dec, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(b.String())))
						msgs <- string(dec)
						say("250 queued")
					case "QUIT":
						say("221 bye")
						return
					default:
						say("250 ok")
					}
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return mail.Config{Host: host, Port: p, Security: mail.None, From: "tv@example.com"}, msgs
}

func TestForgotPassword(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := newBrowser(t, f)

	// Without a mail server the page explains whom to ask.
	if _, _, body := b.do("GET", "/account/forgot", nil); !strings.Contains(body, "This portal cannot send email") {
		t.Errorf("forgot without mail:\n%s", body)
	}

	cfg, msgs := fakeSMTP(t)
	st, _ := f.store.Settings(ctx)
	st.Mail = cfg
	if err := f.store.SaveSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	u, _ := f.store.AuthenticateUser(ctx, "jan", "secret1")
	if err := f.store.SetUserEmail(ctx, u.ID, "jan@example.org"); err != nil {
		t.Fatal(err)
	}

	// The answer is the same for an unknown account.
	_, _, unknown := b.do("POST", "/account/forgot", url.Values{"login": {"nobody"}})
	_, _, known := b.do("POST", "/account/forgot", url.Values{"login": {"jan@example.org"}})
	if !strings.Contains(known, "If an account with this username") || unknown != known {
		t.Errorf("answers differ or are wrong:\n%s\n---\n%s", unknown, known)
	}
	var msg string
	select {
	case msg = <-msgs:
	case <-time.After(5 * time.Second):
		t.Fatal("no reset email sent")
	}
	m := regexp.MustCompile(regexp.QuoteMeta(f.srv.URL) + `/account/reset/([0-9a-f]+)`).FindStringSubmatch(msg)
	if m == nil || !strings.Contains(msg, "Reset your password") {
		t.Fatalf("reset email:\n%s", msg)
	}
	path := "/account/reset/" + m[1]

	if code, _, body := b.do("GET", path, nil); code != http.StatusOK || !strings.Contains(body, "<strong>jan</strong>") {
		t.Fatalf("reset form: %d\n%s", code, body)
	}
	if code, _, _ := b.do("GET", "/account/reset/0123", nil); code != http.StatusNotFound {
		t.Errorf("unknown token: %d", code)
	}
	if code, _, body := b.do("POST", path, url.Values{"password": {"nieuw12"}, "confirm": {"anders1"}}); code != http.StatusBadRequest ||
		!strings.Contains(body, "The passwords do not match.") {
		t.Errorf("mismatch: %d\n%s", code, body)
	}
	// Setting the password logs the user in.
	if code, loc, _ := b.do("POST", path, url.Values{"password": {"nieuw12"}, "confirm": {"nieuw12"}}); code != http.StatusSeeOther ||
		!strings.HasPrefix(loc, "/account?ok=") {
		t.Fatalf("reset: %d %s", code, loc)
	}
	if code, _, _ := b.do("GET", "/account", nil); code != http.StatusOK {
		t.Errorf("not logged in after the reset: %d", code)
	}
	if _, err := f.store.AuthenticateUser(ctx, "jan", "nieuw12"); err != nil {
		t.Errorf("new password: %v", err)
	}
	if code, _, body := b.do("GET", path, nil); code != http.StatusNotFound || !strings.Contains(body, "Request a new link") {
		t.Errorf("used link: %d\n%s", code, body)
	}
}

func TestAccountInDutch(t *testing.T) {
	f := setup(t)
	b := newBrowser(t, f)
	u, _ := url.Parse(f.srv.URL)
	b.client.Jar.SetCookies(u, []*http.Cookie{{Name: i18n.Cookie, Value: "nl"}})
	b.do("POST", "/account/login", url.Values{"username": {"jan"}, "password": {"secret1"}})
	_, _, body := b.do("GET", "/account", nil)
	for _, want := range []string{"Hallo, jan", "Je tv instellen", "Kijkgeschiedenis", "Je abonnement", `lang="nl"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Dutch overview lacks %q:\n%s", want, body)
		}
	}
}
