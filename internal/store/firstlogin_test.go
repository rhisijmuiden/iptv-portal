package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitialCredentialsForgottenAfterPasswordChange(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	pw := GeneratePassword()
	a, err := s.CreateAdmin(ctx, "admin", pw, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteInitialCredentials(s.Dir(), InitialCredentials{Username: "admin", Password: pw}); err != nil {
		t.Fatal(err)
	}
	banner := filepath.Join(s.Dir(), BannerFile)
	os.WriteFile(banner, []byte("IPTV Portal\n  Admin panel:        http://\\4:8080/\n  Username:           admin\n  One-time password:  "+pw+"\n  (change it after your first login)\n\n"), 0o644)

	c, ok, err := ReadInitialCredentials(s.Dir())
	if err != nil || !ok || c.Username != "admin" || c.Password != pw {
		t.Fatalf("ReadInitialCredentials = %+v, %v, %v", c, ok, err)
	}
	sess, _ := s.CreateSession(ctx, a)
	if err := s.ChangeOwnPassword(ctx, sess, pw, "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := ReadInitialCredentials(s.Dir()); ok {
		t.Fatal("one-time login still stored after the password change")
	}
	got, _ := os.ReadFile(banner)
	if strings.Contains(string(got), pw) || strings.Contains(string(got), "Username") {
		t.Fatalf("banner still shows the login:\n%s", got)
	}
	if !strings.Contains(string(got), `Admin panel:        http://\4:8080/`) {
		t.Fatalf("banner lost the address:\n%s", got)
	}
}
