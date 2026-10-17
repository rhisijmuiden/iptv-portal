package store

import (
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
	if st != DefaultSettings {
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
