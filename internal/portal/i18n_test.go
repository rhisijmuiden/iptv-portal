package portal

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/i18n"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func TestInviteInDutch(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	pkgs, _ := f.store.Packages(ctx)
	inv, err := f.store.CreateInvite(ctx, store.Invite{PackageIDs: []int64{pkgs[0].ID}, MaxUses: 2, ValidDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	page := func(method, lang string, form url.Values) string {
		t.Helper()
		req, _ := http.NewRequest(method, f.srv.URL+"/invite/"+inv.Token, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if lang != "" {
			req.AddCookie(&http.Cookie{Name: i18n.Cookie, Value: lang})
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if body := page("GET", "", nil); !strings.Contains(body, "You have been invited") || !strings.Contains(body, `lang="en"`) {
		t.Errorf("invite page is not English by default:\n%s", body)
	}
	if body := page("GET", "nl", nil); !strings.Contains(body, "Je bent uitgenodigd") || !strings.Contains(body, `<option value="nl" selected>`) {
		t.Errorf("invite page with the Dutch cookie:\n%s", body)
	}
	body := page("POST", "nl", url.Values{"username": {"piet"}, "password": {"geheim1"}, "confirm": {"anders1"}})
	if !strings.Contains(body, "De wachtwoorden komen niet overeen.") {
		t.Errorf("error is not in Dutch:\n%s", body)
	}
	body = page("POST", "nl", url.Values{"username": {"piet"}, "password": {"geheim1"}, "confirm": {"geheim1"}})
	if !strings.Contains(body, "Je account is klaar") || !strings.Contains(body, "Je account is geldig tot <strong>") {
		t.Errorf("welcome page is not in Dutch:\n%s", body)
	}
	for _, m := range []string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"} {
		if strings.Contains(body, m) {
			return
		}
	}
	t.Error("the expiry date is not written with a Dutch month")
}

func TestPortalLanguageSwitch(t *testing.T) {
	f := setup(t)
	resp := f.get(t, "/language?lang=nl&next=/invite/abc")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/invite/abc" {
		t.Errorf("switch: %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if c := resp.Cookies(); len(c) != 1 || c[0].Name != i18n.Cookie || c[0].Value != "nl" {
		t.Errorf("cookies %v", c)
	}
	if loc := f.get(t, "/language?lang=nl&next=https://evil.example").Header.Get("Location"); loc != "/" {
		t.Errorf("an outside address is followed: %q", loc)
	}
}
