package backup

import (
	"context"
	"io"
	"strings"
)

// Target is a connection to a remote location.
type Target interface {
	// Upload stores a file. It is written under a temporary name first, so
	// an interrupted upload never looks like a finished backup.
	Upload(ctx context.Context, name string, r io.Reader, size int64) error
	// Download writes the contents of a file to w.
	Download(ctx context.Context, name string, w io.Writer) error
	// List returns the files in the remote directory.
	List(ctx context.Context) ([]File, error)
	Delete(ctx context.Context, name string) error
	Close() error
}

// dialer connects to a location; tests replace it.
var dialer = dial

func dial(ctx context.Context, l Location) (Target, error) {
	if l.Protocol == SCP {
		return dialSCP(ctx, l, false)
	}
	return dialFTP(ctx, l)
}

// Test connects to the location, lists the remote directory and writes and
// deletes a small file. For SCP without a stored host key it accepts the
// server's key and returns its fingerprint, to be stored in HostKey.
func Test(ctx context.Context, l Location) (hostKey string, err error) {
	var t Target
	if l.Protocol == SCP && l.HostKey == "" {
		var s *scpTarget
		if s, err = dialSCP(ctx, l, true); err != nil {
			return "", err
		}
		t, hostKey = s, s.hostKey
	} else if t, err = dialer(ctx, l); err != nil {
		return "", err
	}
	defer t.Close()
	if _, err := t.List(ctx); err != nil {
		return hostKey, err
	}
	const probe = ".iptv-portal-write-test"
	if err := t.Upload(ctx, probe, strings.NewReader("ok\n"), 3); err != nil {
		return hostKey, err
	}
	return hostKey, t.Delete(ctx, probe)
}
