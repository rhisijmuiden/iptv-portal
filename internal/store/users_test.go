package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUsersPackagesAndChannels(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.ImportChannels(ctx, []ImportEntry{
		{Name: "NH", StreamURL: "https://s/nh.m3u8", Group: "North"},
		{Name: "Gld", StreamURL: "https://s/gld.m3u8", Group: "East"},
		{Name: "Loose", StreamURL: "https://s/loose.m3u8"},
	}); err != nil {
		t.Fatal(err)
	}
	cats, _ := s.Categories(ctx)
	var north int64
	for _, c := range cats {
		if c.Name == "North" {
			north = c.ID
		}
	}
	regional, err := s.SavePackage(ctx, Package{Name: "Regional", CategoryIDs: []int64{north}})
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.SavePackage(ctx, Package{Name: "Everything", AllChannels: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavePackage(ctx, Package{Name: "Empty"}); err == nil {
		t.Fatal("package without categories accepted")
	}

	id, err := s.SaveUser(ctx, User{Username: "jan", Enabled: true, PackageIDs: []int64{regional}}, "secret1")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"has/slash", "sp ace1", "short"} {
		if _, err := s.SaveUser(ctx, User{Username: "x" + bad[:1] + "yz", Enabled: true}, bad); err == nil {
			t.Errorf("password %q accepted", bad)
		}
	}
	u, err := s.AuthenticateUser(ctx, "JAN", "secret1")
	if err != nil {
		t.Fatal(err)
	}
	chs, _ := s.UserChannels(ctx, u.ID)
	if len(chs) != 1 || chs[0].Name != "NH" {
		t.Fatalf("regional channels = %+v", chs)
	}
	// Cached path must still reject a wrong password.
	if _, err := s.AuthenticateUser(ctx, "jan", "secret2"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("wrong password err = %v", err)
	}

	u.PackageIDs = []int64{regional, all}
	if _, err := s.SaveUser(ctx, u, ""); err != nil {
		t.Fatal(err)
	}
	if chs, _ := s.UserChannels(ctx, id); len(chs) != 3 {
		t.Fatalf("all channels = %d", len(chs))
	}

	past := time.Now().Add(-time.Hour)
	u.ExpiresAt = &past
	if _, err := s.SaveUser(ctx, u, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateUser(ctx, "jan", "secret1"); !errors.Is(err, ErrUserInactive) {
		t.Fatalf("expired user err = %v", err)
	}
	if active, total, _ := s.CountActiveUsers(ctx); active != 0 || total != 1 {
		t.Fatalf("counts = %d/%d", active, total)
	}
	got, _ := s.User(ctx, id)
	if got.ExpiresAt == nil || got.ExpiresAt.Sub(past).Abs() > time.Second {
		t.Fatalf("expiry not stored: %v", got.ExpiresAt)
	}
	if err := s.SetUserEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}
}

func TestInvites(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	pkg, err := s.SavePackage(ctx, Package{Name: "All", AllChannels: true})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := s.CreateInvite(ctx, Invite{Note: "Family", MaxUses: 1, ValidDays: 30, PackageIDs: []int64{pkg}})
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.RedeemInvite(ctx, inv.Token, "piet", "secret1")
	if err != nil {
		t.Fatal(err)
	}
	if u.Note != "Family" || len(u.PackageIDs) != 1 || u.ExpiresAt == nil ||
		u.ExpiresAt.Before(time.Now().AddDate(0, 0, 29)) {
		t.Fatalf("redeemed user = %+v", u)
	}
	if _, err := s.RedeemInvite(ctx, inv.Token, "klaas", "secret1"); !errors.Is(err, ErrInviteUnusable) {
		t.Fatalf("second use err = %v", err)
	}
	got, _ := s.InviteByToken(ctx, inv.Token)
	if got.Uses != 1 || got.Status() != "used" {
		t.Fatalf("invite after use = %+v", got)
	}

	open, _ := s.CreateInvite(ctx, Invite{MaxUses: 0, PackageIDs: []int64{pkg}})
	if _, err := s.RedeemInvite(ctx, open.Token, "piet", "secret1"); err == nil {
		t.Fatal("duplicate username accepted")
	}
	if _, err := s.RedeemInvite(ctx, open.Token, "klaas", "secret1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeInvite(ctx, open.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemInvite(ctx, open.Token, "kees", "secret1"); !errors.Is(err, ErrInviteUnusable) {
		t.Fatalf("revoked invite err = %v", err)
	}
	if _, err := s.CreateInvite(ctx, Invite{}); err == nil {
		t.Fatal("invite without packages accepted")
	}
}
