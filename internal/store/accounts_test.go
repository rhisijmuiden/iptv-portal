package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAccountSessions(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	id, err := st.SaveUser(ctx, User{Username: "jan", Enabled: false}, "secret1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoginUser(ctx, "jan", "wrong1"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := st.LoginUser(ctx, "nobody", "secret1"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("unknown user: %v", err)
	}
	// A disabled user can still see their account.
	u, err := st.LoginUser(ctx, "jan", "secret1")
	if err != nil || u.ID != id {
		t.Fatalf("login = %v, %v", u, err)
	}
	s1, err := st.CreateUserSession(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := st.CreateUserSession(ctx, u)
	if got, err := st.UserSession(ctx, s1.Token); err != nil || got.User.Username != "jan" || got.CSRF != s1.CSRF {
		t.Fatalf("session = %+v, %v", got, err)
	}

	if err := st.ChangeUserPassword(ctx, s1, "wrong1", "newpass1"); err == nil {
		t.Fatal("changed the password with a wrong current password")
	}
	if err := st.ChangeUserPassword(ctx, s1, "secret1", "bad/pass"); err == nil {
		t.Fatal("accepted a password with a slash")
	}
	if err := st.ChangeUserPassword(ctx, s1, "secret1", "newpass1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UserSession(ctx, s1.Token); err != nil {
		t.Errorf("the session that changed the password was logged out: %v", err)
	}
	if _, err := st.UserSession(ctx, s2.Token); !errors.Is(err, ErrNotFound) {
		t.Errorf("other session still valid: %v", err)
	}
	if _, err := st.LoginUser(ctx, "jan", "newpass1"); err != nil {
		t.Errorf("new password does not work: %v", err)
	}

	// An admin reset logs every account session out.
	if _, err := st.ResetUserPassword(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UserSession(ctx, s1.Token); !errors.Is(err, ErrNotFound) {
		t.Errorf("session survived an admin password reset: %v", err)
	}

	if err := st.SetUserEmail(ctx, id, "not an address"); err == nil {
		t.Error("accepted an invalid email address")
	}
	if err := st.SetUserEmail(ctx, id, " Jan@Example.org "); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.User(ctx, id); u.Email != "Jan@Example.org" {
		t.Errorf("email = %q", u.Email)
	}

	// Expired sessions are purged.
	s3, _ := st.CreateUserSession(ctx, u)
	st.db.ExecContext(ctx, `UPDATE user_sessions SET expires_at = ? WHERE token = ?`, time.Now().Add(-time.Minute).UTC(), s3.Token)
	if _, err := st.UserSession(ctx, s3.Token); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session = %v", err)
	}
	if err := st.PurgeExpiredSessions(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_sessions`).Scan(&n)
	if n != 0 {
		t.Errorf("%d sessions left after purge", n)
	}
}

func TestPasswordReset(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	id, err := st.SaveUser(ctx, User{Username: "jan", Email: "jan@example.org", Enabled: true}, "secret1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveUser(ctx, User{Username: "piet", Enabled: true}, "secret1"); err != nil {
		t.Fatal(err)
	}
	// No account, or no email address: no link, and no error either.
	for _, login := range []string{"nobody", "piet", ""} {
		if rs, err := st.CreatePasswordResets(ctx, login); err != nil || len(rs) != 0 {
			t.Errorf("%q: %v, %v", login, rs, err)
		}
	}
	old, err := st.CreatePasswordResets(ctx, "JAN@example.org")
	if err != nil || len(old) != 1 || old[0].User.ID != id {
		t.Fatalf("resets by email = %v, %v", old, err)
	}
	rs, err := st.CreatePasswordResets(ctx, "jan")
	if err != nil || len(rs) != 1 {
		t.Fatalf("resets by username = %v, %v", rs, err)
	}
	if _, err := st.PasswordResetUser(ctx, old[0].Token); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("an earlier link still works: %v", err)
	}
	var stored string
	st.db.QueryRowContext(ctx, `SELECT token_hash FROM password_resets`).Scan(&stored)
	if stored == rs[0].Token {
		t.Error("the token is stored in plain text")
	}

	sess, _ := st.CreateUserSession(ctx, User{ID: id})
	if _, err := st.ResetUserPasswordWithToken(ctx, rs[0].Token, "bad pass"); err == nil {
		t.Error("accepted a password with a space")
	}
	u, err := st.ResetUserPasswordWithToken(ctx, rs[0].Token, "newpass1")
	if err != nil || u.ID != id {
		t.Fatalf("reset = %v, %v", u, err)
	}
	if _, err := st.AuthenticateUser(ctx, "jan", "newpass1"); err != nil {
		t.Errorf("player login with the new password: %v", err)
	}
	if _, err := st.ResetUserPasswordWithToken(ctx, rs[0].Token, "other12"); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("link used twice: %v", err)
	}
	if _, err := st.UserSession(ctx, sess.Token); !errors.Is(err, ErrNotFound) {
		t.Errorf("session survived a password reset: %v", err)
	}

	expired, _ := st.CreatePasswordResets(ctx, "jan")
	st.db.ExecContext(ctx, `UPDATE password_resets SET expires_at = ?`, time.Now().Add(-time.Minute).UTC())
	if _, err := st.PasswordResetUser(ctx, expired[0].Token); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("expired link: %v", err)
	}
}
