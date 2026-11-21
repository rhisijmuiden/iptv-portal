package store

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The installer keeps the first admin login in the data directory until
// the admin has chosen a new password, so the console can show it: the
// login banner of the console (/etc/issue.d links to BannerFile) and the
// message after logging in on the shell (iptv-portal login-info).
const (
	// CredentialsFile holds the username and one-time password.
	CredentialsFile = "initial-credentials"
	// BannerFile is an agetty issue file with the admin panel address and,
	// until the password is changed, the one-time login.
	BannerFile = "login-banner.issue"
)

// InitialCredentials is the first admin login made by admin-reset.
type InitialCredentials struct {
	Username string
	Password string
}

// WriteInitialCredentials stores the one-time login in dir, readable only
// by its owner.
func WriteInitialCredentials(dir string, c InitialCredentials) error {
	data := "username=" + c.Username + "\npassword=" + c.Password + "\n"
	path := filepath.Join(dir, CredentialsFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadInitialCredentials returns the stored one-time login, or ok false
// when there is none (the password has been changed).
func ReadInitialCredentials(dir string) (c InitialCredentials, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, CredentialsFile))
	if errors.Is(err, os.ErrNotExist) {
		return c, false, nil
	} else if err != nil {
		return c, false, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, value, _ := strings.Cut(sc.Text(), "=")
		switch key {
		case "username":
			c.Username = value
		case "password":
			c.Password = value
		}
	}
	return c, c.Username != "" && c.Password != "", nil
}

// forgetInitialCredentials removes the one-time login of username from the
// data directory once it is no longer valid, and takes the password out of
// the console banner. It is best effort: the files are only a convenience.
func (s *Store) forgetInitialCredentials(username string) {
	c, ok, err := ReadInitialCredentials(s.dir)
	if err != nil || !ok || c.Username != username {
		return
	}
	os.Remove(filepath.Join(s.dir, CredentialsFile))
	banner := filepath.Join(s.dir, BannerFile)
	data, err := os.ReadFile(banner)
	if err != nil {
		return
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, c.Password) || strings.HasPrefix(strings.TrimSpace(line), "Username:") ||
			strings.Contains(line, "change it after") {
			continue
		}
		out = append(out, line)
	}
	os.WriteFile(banner, []byte(strings.Join(out, "\n")), 0o644)
}
