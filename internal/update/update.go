// Package update checks GitHub for new releases and replaces the running
// binary with a newer one.
package update

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultAPI lists the releases of the project on GitHub.
const DefaultAPI = "https://api.github.com/repos/rhisijmuiden/iptv-portal/releases"

// Release is one published version.
type Release struct {
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	Notes       string    `json:"body"`
	URL         string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []Asset   `json:"assets"`
}

// Asset is a downloadable file of a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Status is the result of the last check.
type Status struct {
	Current   string
	CheckedAt time.Time
	Releases  []Release // newest first, without drafts and pre-releases
	Error     string
}

// Latest returns the newest release, if any.
func (s Status) Latest() *Release {
	if len(s.Releases) == 0 {
		return nil
	}
	return &s.Releases[0]
}

// Available reports whether a newer release than the running one exists.
func (s Status) Available() bool {
	l := s.Latest()
	return l != nil && Newer(l.Tag, s.Current)
}

// Updater checks for and installs releases.
type Updater struct {
	api     string
	current string
	exe     string // path of the running binary
	client  *http.Client

	mu      sync.Mutex
	status  Status
	running sync.Mutex // held while an update is being installed
}

// New creates an updater for the running version. api is the GitHub
// releases endpoint (DefaultAPI unless testing).
func New(api, current string) *Updater {
	exe, err := os.Executable()
	if err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
	}
	return &Updater{api: api, current: current, exe: exe,
		client: &http.Client{Timeout: 5 * time.Minute},
		status: Status{Current: current}}
}

// Status returns the result of the last check.
func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.status
}

// Check fetches the list of releases.
func (u *Updater) Check(ctx context.Context) (Status, error) {
	st := Status{Current: u.current, CheckedAt: time.Now()}
	rels, err := u.fetch(ctx)
	if err != nil {
		st.Error = err.Error()
		u.mu.Lock()
		st.Releases = u.status.Releases // keep what we knew
		u.status = st
		u.mu.Unlock()
		return st, err
	}
	for _, r := range rels {
		if !r.Draft && !r.Prerelease {
			st.Releases = append(st.Releases, r)
		}
	}
	u.mu.Lock()
	u.status = st
	u.mu.Unlock()
	return st, nil
}

func (u *Updater) fetch(ctx context.Context) ([]Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.api+"?per_page=30", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "iptv-portal/"+u.current)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := u.client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("checking for updates failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("checking for updates failed: GitHub answered %s", resp.Status)
	}
	var rels []Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 5<<20)).Decode(&rels); err != nil {
		return nil, fmt.Errorf("checking for updates failed: %w", err)
	}
	return rels, nil
}

// Loop checks every interval while enabled() reports true, until ctx is
// done.
func (u *Updater) Loop(ctx context.Context, every time.Duration, enabled func() bool) {
	t := time.NewTimer(time.Minute) // first check shortly after start
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if enabled() {
			u.Check(ctx)
		}
		t.Reset(every)
	}
}

// ErrDevBuild is returned when updating a build without a release version.
var ErrDevBuild = errors.New("this is a development build; install a release first")

// Install downloads release tag, verifies it and replaces the running
// binary. The previous binary is kept next to it with the suffix ".old".
// The caller restarts the process afterwards.
func (u *Updater) Install(ctx context.Context, tag string) error {
	if !u.running.TryLock() {
		return errors.New("an update is already being installed")
	}
	defer u.running.Unlock()
	if _, ok := parseVersion(u.current); !ok {
		return ErrDevBuild
	}
	var rel *Release
	for _, r := range u.Status().Releases {
		if r.Tag == tag {
			r := r
			rel = &r
			break
		}
	}
	if rel == nil {
		return fmt.Errorf("release %s not found, check for updates first", tag)
	}
	name := fmt.Sprintf("iptv-portal_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive, sums := assetURL(rel, name), assetURL(rel, "checksums.txt")
	if archive == "" {
		return fmt.Errorf("release %s has no download for %s/%s", tag, runtime.GOOS, runtime.GOARCH)
	}
	if sums == "" {
		return fmt.Errorf("release %s has no checksums.txt", tag)
	}

	dir := filepath.Dir(u.exe)
	tmp, err := os.CreateTemp(dir, ".iptv-portal-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (%v); update with the installer instead", dir, err)
	}
	defer os.Remove(tmp.Name())

	want, err := u.checksum(ctx, sums, name)
	if err != nil {
		return err
	}
	got, err := u.downloadBinary(ctx, archive, tmp)
	tmp.Close()
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("the download does not match its checksum")
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	// Make sure the new binary actually runs on this machine.
	out, err := exec.CommandContext(ctx, tmp.Name(), "version").Output()
	if err != nil {
		return fmt.Errorf("the new version does not start: %v", err)
	}
	if v := strings.TrimSpace(string(out)); v != tag {
		return fmt.Errorf("the download reports version %q instead of %s", v, tag)
	}

	old := u.exe + ".old"
	os.Remove(old)
	if err := os.Link(u.exe, old); err != nil {
		// Hard links can fail on some file systems; fall back to a copy.
		if err := copyFile(u.exe, old); err != nil {
			return fmt.Errorf("keeping the current version failed: %w", err)
		}
	}
	if err := os.Rename(tmp.Name(), u.exe); err != nil {
		return fmt.Errorf("replacing the binary failed: %w", err)
	}
	return nil
}

func assetURL(r *Release, name string) string {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL
		}
	}
	return ""
}

func (u *Updater) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "iptv-portal/"+u.current)
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download failed: %s", resp.Status)
	}
	return resp, nil
}

// checksum returns the expected sha256 of file from a checksums.txt.
func (u *Updater) checksum(ctx context.Context, url, file string) (string, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == file {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", file)
}

// downloadBinary writes the iptv-portal file from the release archive to
// dst and returns the sha256 of the archive.
func (u *Updater) downloadBinary(ctx context.Context, url string, dst io.Writer) (string, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	h := sha256.New()
	zr, err := gzip.NewReader(io.TeeReader(io.LimitReader(resp.Body, 200<<20), h))
	if err != nil {
		return "", fmt.Errorf("invalid download: %w", err)
	}
	tr := tar.NewReader(zr)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("invalid download: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == "iptv-portal" && !found {
			if _, err := io.Copy(dst, tr); err != nil {
				return "", err
			}
			found = true
		}
	}
	// Read the rest so the hash covers the whole archive.
	io.Copy(io.Discard, zr)
	if !found {
		return "", errors.New("the download does not contain the iptv-portal program")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// parseVersion parses "v1.2.3" (the "v" is optional). Versions with a
// suffix such as "-rc1" or "-dirty" are not release versions.
func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// Newer reports whether version a is newer than b. A non-release b (such
// as "dev") is never considered older, so development builds do not
// nag about updates.
func Newer(a, b string) bool {
	va, ok1 := parseVersion(a)
	vb, ok2 := parseVersion(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}
