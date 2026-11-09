package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestNormalizeMAC(t *testing.T) {
	for in, want := range map[string]string{
		"00:1a:79:12:34:56": "00:1A:79:12:34:56", "00-1A-79-12-34-56": "00:1A:79:12:34:56",
		"001a79123456": "00:1A:79:12:34:56", " 001A.7912.3456 ": "00:1A:79:12:34:56",
	} {
		if got, err := NormalizeMAC(in); err != nil || got != want {
			t.Errorf("NormalizeMAC(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "00:1a:79:12:34", "zz:1a:79:12:34:56", "00:00:00:00:00:00", "001a7912345678"} {
		if _, err := NormalizeMAC(in); err == nil {
			t.Errorf("NormalizeMAC(%q) accepted", in)
		}
	}
}

func TestUserDevices(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, err := s.SaveUser(ctx, User{Username: "jan", Enabled: true}, "secret1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUserDevice(ctx, id, "Box", "00:1A:79:00:00:01"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUserDevice(ctx, id, "Again", "001a79000001"); err == nil {
		t.Error("duplicate MAC accepted")
	}
	for i := 2; len(mustDevices(t, s, id)) < MaxUserDevices; i++ {
		if _, err := s.AddUserDevice(ctx, id, "Box", fmt.Sprintf("00:1A:79:00:00:%02X", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddUserDevice(ctx, id, "One too many", "00:1A:79:00:01:00"); err == nil {
		t.Error("more than the maximum accepted")
	}
}

func mustDevices(t *testing.T, s *Store, id int64) []Device {
	t.Helper()
	d, err := s.UserDevices(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestGuidesRestore(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	list, _ := s.Guides(ctx, false)
	if len(list) == 0 {
		t.Fatal("no default guides")
	}
	if err := s.DeleteGuide(ctx, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RestoreGuides(ctx); err != nil || n != 1 {
		t.Errorf("RestoreGuides = %d, %v", n, err)
	}
	if n, _ := s.RestoreGuides(ctx); n != 0 {
		t.Errorf("second restore added %d", n)
	}
}

func TestPackageRequestReplace(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	basic, _ := s.SavePackage(ctx, Package{Name: "Basic", AllChannels: true, Public: true})
	plus, _ := s.SavePackage(ctx, Package{Name: "Plus", AllChannels: true, OnDemand: true, Public: true})
	id, _ := s.SaveUser(ctx, User{Username: "jan", Enabled: true, PackageIDs: []int64{basic}}, "secret1")
	if _, err := s.RequestPackage(ctx, id, basic, ""); err == nil {
		t.Error("requesting an own package accepted")
	}
	r, err := s.RequestPackage(ctx, id, plus, "swap please")
	if err != nil {
		t.Fatal(err)
	}
	if r, err = s.DecidePackageRequest(ctx, r.ID, true, true, ""); err != nil || r.Status != RequestApproved {
		t.Fatalf("approve: %v %v", r.Status, err)
	}
	u, _ := s.User(ctx, id)
	if len(u.PackageIDs) != 1 || u.PackageIDs[0] != plus {
		t.Errorf("packages after replace: %v", u.PackageIDs)
	}
	if _, err := s.DecidePackageRequest(ctx, r.ID, false, false, ""); err == nil {
		t.Error("answered twice")
	}
	// Cancel
	r2, _ := s.RequestPackage(ctx, id, basic, "")
	if err := s.CancelPackageRequest(ctx, id+1, r2.ID); err == nil {
		t.Error("another user cancelled the request")
	}
	if err := s.CancelPackageRequest(ctx, id, r2.ID); err != nil {
		t.Error(err)
	}
}

func TestPreferencesApply(t *testing.T) {
	tv := func(id, cat int64) Channel { return Channel{ID: id, CategoryID: cat, Kind: KindTV} }
	radio := func(id, cat int64) Channel { return Channel{ID: id, CategoryID: cat, Kind: KindRadio} }
	chs := []Channel{tv(1, 10), tv(2, 10), tv(3, 20), tv(4, 0), radio(5, 30), tv(6, 40)}
	ids := func(list []Channel) []int64 {
		var out []int64
		for _, c := range list {
			out = append(out, c.ID)
		}
		return out
	}
	now := time.Now()
	p := Preferences{Categories: map[int64]UserCategory{
		40: {Position: 1}, 20: {Position: 2}, 30: {Position: 1},
		10: {Hidden: true}, 50: {Locked: true},
	}, Favourites: []int64{2, 99, 5}}
	v := p.Apply(chs, now)
	if got := ids(v.Channels); !slices.Equal(got, []int64{6, 3, 4, 5}) {
		t.Errorf("channels = %v", got)
	}
	if got := ids(v.Favourites); !slices.Equal(got, []int64{2, 5}) {
		t.Errorf("favourites = %v", got)
	}
	if got := ids(v.Listed()); !slices.Equal(got, []int64{2, 5, 6, 3, 4}) {
		t.Errorf("listed = %v", got)
	}
	if v.Locked {
		t.Error("locked without locks on the user's groups")
	}
	p.Categories[20] = UserCategory{Locked: true}
	v = p.Apply(chs, now)
	if _, ok := v.Playable[3]; ok || !v.Locked {
		t.Errorf("locked channel playable: %+v", v)
	}
	until := now.Add(time.Minute)
	p.Parental.UnlockedUntil = &until
	if v = p.Apply(chs, now); v.Playable[3].ID != 3 || v.Locked {
		t.Errorf("unlocked: %+v", v)
	}
}
