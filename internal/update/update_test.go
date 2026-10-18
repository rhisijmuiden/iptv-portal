package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v1.2.0", "v1.1.9", true},
		{"v1.10.0", "v1.9.0", true},
		{"v1.2.0", "v1.2.0", false},
		{"v1.1.0", "v1.2.0", false},
		{"v2.0.0", "dev", false},
		{"v2.0.0-rc.1", "v1.0.0", true},
		{"v1.0.0", "v1.0.0-rc.1", true},
		{"v1.0.0-rc.1", "v1.0.0", false},
		{"v1.0.0-rc.2", "v1.0.0-rc.1", true},
		{"v1.0.0-rc.10", "v1.0.0-rc.9", true},
		{"v1.0.0-rc.1", "v1.0.0-beta.3", true},
		{"v1.0.0-beta.1", "v1.0.0-alpha.2", true},
		{"v1.0.0-rc.1", "v0.9.0", true},
		{"v1.0.0-rc.1", "v1.0.0-rc.1", false},
		{"v2.0.0", "v1.0.0-5-gabc123", false},
		{"v2.0.0", "v1.0.0-dirty", false},
		{"v2.0.0-dirty", "v1.0.0", false},
		{"v1.0.1", "1.0.0", true},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestIsPrerelease(t *testing.T) {
	for tag, want := range map[string]bool{
		"v1.0.0-rc.1": true, "v1.0.0-beta.2": true, "v1.0.0": false,
		"dev": false, "v1.0.0-3-gabc123": false,
	} {
		if got := IsPrerelease(tag); got != want {
			t.Errorf("IsPrerelease(%q) = %v", tag, got)
		}
	}
}

func TestRenderNotes(t *testing.T) {
	md := "## What's Changed\n* Add users by @rhisijmuiden in https://github.com/rhisijmuiden/iptv-portal/pull/1\n* <script>x</script> **bold** `code`\n\n**Full Changelog**: [v0.1.0...v0.2.0](https://github.com/x/y/compare/v0.1.0...v0.2.0)"
	got := string(RenderNotes(md))
	for _, want := range []string{
		"<h3>What&#39;s Changed</h3>",
		`<a href="https://github.com/rhisijmuiden/iptv-portal/pull/1" target="_blank" rel="noopener">#1</a>`,
		"&lt;script&gt;x&lt;/script&gt; <strong>bold</strong> <code>code</code>",
		`<a href="https://github.com/x/y/compare/v0.1.0...v0.2.0" target="_blank" rel="noopener">v0.1.0...v0.2.0</a>`,
		"</ul>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>") {
		t.Fatal("HTML passed through")
	}
}

// fakeRelease serves a GitHub-like releases API with one release whose
// binary is a shell script printing its version.
func fakeRelease(t *testing.T, tag string, corrupt bool) *httptest.Server {
	t.Helper()
	script := []byte("#!/bin/sh\necho " + tag + "\n")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: "iptv-portal", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg})
	tw.Write(script)
	tw.Close()
	zw.Close()
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	name := fmt.Sprintf("iptv-portal_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	checksum := hex.EncodeToString(sum[:])
	if corrupt {
		checksum = strings.Repeat("0", 64)
	}

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			json.NewEncoder(w).Encode([]map[string]any{
				{"tag_name": "v9.0.0-rc1", "prerelease": true},
				{"tag_name": tag, "name": tag, "body": "## What's Changed\n* Things", "assets": []map[string]string{
					{"name": name, "browser_download_url": srv.URL + "/dl/" + name},
					{"name": "checksums.txt", "browser_download_url": srv.URL + "/dl/checksums.txt"},
				}},
				{"tag_name": "v1.0.0", "name": "v1.0.0"},
			})
		case "/dl/" + name:
			w.Write(archive)
		case "/dl/checksums.txt":
			fmt.Fprintf(w, "%s  %s\n", checksum, name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckAndInstall(t *testing.T) {
	srv := fakeRelease(t, "v1.1.0", false)
	dir := t.TempDir()
	exe := filepath.Join(dir, "iptv-portal")
	os.WriteFile(exe, []byte("old binary"), 0o755)

	u := New(srv.URL+"/releases", "v1.0.0")
	u.exe = exe
	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Releases) != 2 || st.Latest().Tag != "v1.1.0" || !st.Available() {
		t.Fatalf("status = %+v", st)
	}
	if err := u.Install(context.Background(), "v1.1.0"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old binary" {
		t.Fatalf("old binary not kept: %q", b)
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "echo v1.1.0") {
		t.Fatalf("binary not replaced: %q", b)
	}
}

func TestChannels(t *testing.T) {
	srv := fakeRelease(t, "v1.1.0", false)
	u := New(srv.URL+"/releases", "v1.0.0")
	st, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Channel != Stable || st.Latest().Tag != "v1.1.0" {
		t.Fatalf("stable status = %+v", st)
	}
	for _, r := range st.Releases {
		if r.Prerelease {
			t.Fatalf("stable channel offers pre-release %s", r.Tag)
		}
	}

	u.SetChannel(Test)
	if got := u.Status(); got.Channel != Test || len(got.Releases) != 0 {
		t.Fatalf("switching channel kept the old releases: %+v", got)
	}
	st, err = u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Releases) != 3 || st.Latest().Tag != "v9.0.0-rc1" || !st.Available() {
		t.Fatalf("test status = %+v", st)
	}

	// A pre-release on the test channel moves on to the stable release.
	u = New(srv.URL+"/releases", "v1.1.0-rc.1")
	st, _ = u.Check(context.Background())
	if !st.Available() || st.Latest().Tag != "v1.1.0" {
		t.Fatalf("pre-release on stable channel: %+v", st)
	}
}

func TestParseChannel(t *testing.T) {
	if ParseChannel("test") != Test || ParseChannel("stable") != Stable || ParseChannel("") != Stable || ParseChannel("x") != Stable {
		t.Fatal("ParseChannel")
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	srv := fakeRelease(t, "v1.1.0", true)
	dir := t.TempDir()
	exe := filepath.Join(dir, "iptv-portal")
	os.WriteFile(exe, []byte("old binary"), 0o755)
	u := New(srv.URL+"/releases", "v1.0.0")
	u.exe = exe
	u.Check(context.Background())
	if err := u.Install(context.Background(), "v1.1.0"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old binary" {
		t.Fatal("binary replaced despite bad checksum")
	}
}

func TestDevBuildDoesNotInstall(t *testing.T) {
	srv := fakeRelease(t, "v1.1.0", false)
	u := New(srv.URL+"/releases", "dev")
	u.exe = filepath.Join(t.TempDir(), "iptv-portal")
	st, _ := u.Check(context.Background())
	if st.Available() {
		t.Fatal("dev build reports an update")
	}
	if err := u.Install(context.Background(), "v1.1.0"); err != ErrDevBuild {
		t.Fatalf("err = %v", err)
	}
}
