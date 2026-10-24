package store

import (
	"github.com/rhisijmuiden/iptv-portal/internal/mail"

	"context"
	"errors"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSettingsDefaultsAndValidation(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	st, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.PortalName != DefaultSettings.PortalName || st.AdminPort != 8080 || st.PortalPort != 80 || len(st.EPGURLs) != 0 {
		t.Fatalf("defaults = %+v", st)
	}
	st.PublicURL = "https://tv.example.com/"
	st.AdminPort = 9000
	if err := s.SaveSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Settings(ctx)
	if got.PublicURL != "https://tv.example.com" || got.AdminPort != 9000 || got.PortalPort != 80 {
		t.Fatalf("saved = %+v", got)
	}
	if added, err := s.AddEPGURL(ctx, "https://epg.example/guide.xml.gz"); err != nil || !added {
		t.Fatalf("AddEPGURL = %v, %v", added, err)
	}
	if added, _ := s.AddEPGURL(ctx, "https://epg.example/guide.xml.gz"); added {
		t.Fatal("duplicate guide URL added")
	}
	if got, _ := s.Settings(ctx); len(got.EPGURLs) != 1 || got.AdminPort != 9000 {
		t.Fatalf("after AddEPGURL = %+v", got)
	}
	if err := s.SetEPGURLs(ctx, []string{"ftp://x"}); err == nil {
		t.Fatal("ftp guide URL accepted")
	}
	m := mail.Config{Host: "smtp.example.com", Security: mail.TLS, Username: "u", Password: "p", From: "tv@example.com"}
	if err := s.SetMail(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Settings(ctx); got.Mail.Port != 465 || got.Mail.Password != "p" || !got.Mail.Configured() || got.AdminPort != 9000 {
		t.Fatalf("after SetMail = %+v", got)
	}
	if err := s.SetMail(ctx, mail.Config{Host: "smtp.example.com"}); err == nil {
		t.Fatal("mail server without sender accepted")
	}
	for _, bad := range []Settings{
		{PortalName: "x", AdminPort: 80, PortalPort: 80},
		{PortalName: "x", AdminPort: 0, PortalPort: 80},
		{PortalName: "", AdminPort: 8080, PortalPort: 80},
		{PortalName: "x", PublicURL: "tv.example.com", AdminPort: 8080, PortalPort: 80},
	} {
		if err := s.SaveSettings(ctx, bad); err == nil {
			t.Errorf("SaveSettings(%+v) accepted", bad)
		}
	}
}

func TestOneTimePassword(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	pw := GeneratePassword()
	if _, err := s.CreateAdmin(ctx, "admin", pw, true); err != nil {
		t.Fatal(err)
	}
	a, err := s.Authenticate(ctx, "Admin", pw)
	if err != nil || !a.MustChangePassword {
		t.Fatalf("Authenticate = %+v, %v", a, err)
	}
	if _, err := s.Authenticate(ctx, "admin", "wrong-password"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("wrong password err = %v", err)
	}
	sess, _ := s.CreateSession(ctx, a)
	other, _ := s.CreateSession(ctx, a)
	if err := s.ChangeOwnPassword(ctx, sess, pw, pw); err == nil {
		t.Fatal("reusing the one-time password was accepted")
	}
	if err := s.ChangeOwnPassword(ctx, sess, pw, "new-password"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Session(ctx, sess.Token)
	if err != nil || got.Admin.MustChangePassword {
		t.Fatalf("session after change = %+v, %v", got, err)
	}
	if _, err := s.Session(ctx, other.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other session still valid: %v", err)
	}
	// admin-reset sets a new one-time password and logs everyone out.
	if err := s.SetAdminPassword(ctx, "admin", "one-time-123", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, sess.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session survived reset: %v", err)
	}
	if a, _ := s.Authenticate(ctx, "admin", "one-time-123"); !a.MustChangePassword {
		t.Fatal("reset password is not one-time")
	}
}

func TestImportChannels(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	in := []ImportEntry{
		{Name: "NH", StreamURL: "https://s/nh.m3u8", EPGID: "nh.nl", Group: "Noord-Holland"},
		{Name: "Gld", StreamURL: "https://s/gld.m3u8", Group: "Gelderland"},
		{Name: "Kapot", StreamURL: "rtmp://s/x"},
	}
	r, err := s.ImportChannels(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 2 || r.Updated != 0 || len(r.Skipped) != 1 {
		t.Fatalf("first import = %+v", r)
	}
	in[0].Name = "NH Nieuws"
	in[0].EPGID = "" // an empty value must not wipe the stored one
	r, err = s.ImportChannels(ctx, in[:1])
	if err != nil || r.Added != 0 || r.Updated != 1 {
		t.Fatalf("second import = %+v, %v", r, err)
	}
	chs, err := s.Channels(ctx, ChannelFilter{Search: "nieuws"})
	if err != nil || len(chs) != 1 {
		t.Fatalf("search = %+v, %v", chs, err)
	}
	if c := chs[0]; c.Name != "NH Nieuws" || c.EPGID != "nh.nl" || c.Category != "Noord-Holland" || !c.Enabled {
		t.Fatalf("channel = %+v", c)
	}
	cats, _ := s.Categories(ctx)
	if len(cats) != 2 {
		t.Fatalf("categories = %+v", cats)
	}
	if err := s.DeleteCategory(ctx, chs[0].CategoryID); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Channel(ctx, chs[0].ID); c.CategoryID != 0 {
		t.Fatalf("channel kept deleted category: %+v", c)
	}
	if uncategorised, _ := s.Channels(ctx, ChannelFilter{CategoryID: -1}); len(uncategorised) != 1 {
		t.Fatalf("uncategorised = %+v", uncategorised)
	}
}

func TestAddCategoriesToPackages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	keep, err := s.SaveCategory(ctx, Category{Name: "Regional"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.SavePackage(ctx, Package{Name: "Basic", CategoryIDs: []int64{keep}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddCategoriesToPackages(ctx, []int64{p, 999}, KindTV, []string{"News", "Regional"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Package(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CategoryIDs) != 2 {
		t.Fatalf("package categories = %v", got.CategoryIDs)
	}
	urls, err := s.StreamURLs(ctx)
	if err != nil || len(urls) != 0 {
		t.Fatalf("stream urls = %v, %v", urls, err)
	}
}

func TestUpdateChannel(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	st, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.UpdateChannel != UpdateChannelStable {
		t.Fatalf("default channel = %q", st.UpdateChannel)
	}
	if err := s.SetUpdateChannel(ctx, UpdateChannelTest); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Settings(ctx); st.UpdateChannel != UpdateChannelTest {
		t.Fatalf("channel = %q", st.UpdateChannel)
	}
	if err := s.SetUpdateChannel(ctx, "nightly"); err == nil {
		t.Fatal("unknown channel accepted")
	}
}

func TestRadioKinds(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	tv, err := s.EnsureCategory(ctx, KindTV, "News")
	if err != nil {
		t.Fatal(err)
	}
	// A radio category with the name of a TV category gets " (Radio)".
	radio, err := s.EnsureCategory(ctx, KindRadio, "News")
	if err != nil || radio == tv {
		t.Fatalf("radio category %d, %v", radio, err)
	}
	if again, _ := s.EnsureCategory(ctx, KindRadio, "News"); again != radio {
		t.Errorf("second lookup gave %d, want %d", again, radio)
	}
	cats, _ := s.CategoriesOf(ctx, KindRadio)
	if len(cats) != 1 || cats[0].Name != "News (Radio)" || cats[0].Kind != KindRadio {
		t.Fatalf("radio categories = %+v", cats)
	}

	// Importing by source id updates the station even when its stream
	// moved.
	in := []ImportEntry{{Kind: KindRadio, Name: "R1", StreamURL: "https://a.example/old.mp3", Group: "News", SourceID: "u1"}}
	if res, err := s.ImportChannels(ctx, in); err != nil || res.Added != 1 {
		t.Fatalf("import %+v, %v", res, err)
	}
	in[0].StreamURL, in[0].Name = "https://a.example/new.mp3", "Radio 1"
	if res, err := s.ImportChannels(ctx, in); err != nil || res.Updated != 1 || res.Added != 0 {
		t.Fatalf("re-import %+v, %v", res, err)
	}
	chs, _ := s.Channels(ctx, ChannelFilter{Kind: KindRadio})
	if len(chs) != 1 || chs[0].StreamURL != "https://a.example/new.mp3" || chs[0].Name != "Radio 1" ||
		chs[0].CategoryID != radio || !chs[0].IsRadio() || chs[0].SourceID != "u1" {
		t.Fatalf("stations = %+v", chs)
	}
	if ids, _ := s.SourceIDs(ctx, KindRadio); !ids["u1"] {
		t.Errorf("source ids = %v", ids)
	}
	if tvs, _ := s.Channels(ctx, ChannelFilter{Kind: KindTV}); len(tvs) != 0 {
		t.Errorf("TV channels = %+v", tvs)
	}
}
