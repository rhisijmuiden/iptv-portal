package backup

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"
)

const dialTimeout = 30 * time.Second

type ftpTarget struct {
	c *ftp.ServerConn
}

func dialFTP(ctx context.Context, l Location) (*ftpTarget, error) {
	cfg := tlsConfig(l)
	opts := []ftp.DialOption{ftp.DialWithTimeout(dialTimeout), ftp.DialWithDialFunc(dialFunc(ctx, l, cfg))}
	switch l.Protocol {
	case FTPS:
		opts = append(opts, ftp.DialWithExplicitTLS(cfg))
	case FTPSImplicit:
		opts = append(opts, ftp.DialWithTLS(cfg))
	}
	c, err := ftp.Dial(l.addr(), opts...)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", l.addr(), err)
	}
	user := l.Username
	if user == "" {
		user = "anonymous"
	}
	if err := c.Login(user, l.Password); err != nil {
		c.Quit()
		return nil, fmt.Errorf("logging in: %w", err)
	}
	t := &ftpTarget{c: c}
	if err := t.enterDir(l.Dir); err != nil {
		c.Quit()
		return nil, err
	}
	return t, nil
}

// dialFunc opens the control connection and then every data connection.
// With TLS, data connections are wrapped in closeNotifyConn.
func dialFunc(ctx context.Context, l Location, cfg *tls.Config) func(network, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: dialTimeout}
	control := true
	return func(network, addr string) (net.Conn, error) {
		dctx := ctx
		if !control {
			dctx = context.Background() // ctx only bounds connecting; transfers are watched separately
		}
		conn, err := d.DialContext(dctx, network, addr)
		if err != nil {
			return nil, err
		}
		first := control
		control = false
		switch {
		case first && l.Protocol == FTPSImplicit:
			tc := tls.Client(conn, cfg)
			if err := tc.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			return tc, nil
		case first || l.Protocol == FTP:
			return conn, nil // explicit TLS starts on the control connection after AUTH TLS
		}
		return &closeNotifyConn{tls.Client(conn, cfg)}, nil
	}
}

// closeNotifyConn ends a TLS data connection cleanly. With TLS 1.3 the
// server sends session tickets after the handshake; an upload never reads
// them, and closing a socket with unread data resets the connection, which
// can make the server drop the end of the file. So Close first signals the
// end of the data, then reads until the server closes its side.
type closeNotifyConn struct{ *tls.Conn }

func (c *closeNotifyConn) Close() error {
	if c.Conn.CloseWrite() == nil {
		c.SetReadDeadline(time.Now().Add(dialTimeout))
		io.Copy(io.Discard, c.Conn)
	}
	return c.Conn.Close()
}

// enterDir changes to dir, creating the parts that are missing.
func (t *ftpTarget) enterDir(dir string) error {
	if dir == "" {
		return nil
	}
	if strings.HasPrefix(dir, "/") {
		if err := t.c.ChangeDir("/"); err != nil {
			return fmt.Errorf("opening directory /: %w", err)
		}
	}
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		if part == "" || part == "." {
			continue
		}
		if t.c.ChangeDir(part) == nil {
			continue
		}
		if err := t.c.MakeDir(part); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
		if err := t.c.ChangeDir(part); err != nil {
			return fmt.Errorf("opening directory %s: %w", dir, err)
		}
	}
	return nil
}

// tlsConfig verifies the server certificate as usual, or, when a
// fingerprint is set, accepts exactly that certificate. A failed check
// reports the fingerprint so it can be trusted.
func tlsConfig(l Location) *tls.Config {
	return &tls.Config{
		ServerName:         l.Host,
		MinVersion:         tls.VersionTLS12,
		ClientSessionCache: tls.NewLRUClientSessionCache(4), // many servers require session reuse on data connections
		InsecureSkipVerify: true,                            // checked in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the server sent no certificate")
			}
			leaf := cs.PeerCertificates[0]
			fp := CertFingerprint(leaf)
			if l.Fingerprint != "" {
				if fp != l.Fingerprint {
					return fmt.Errorf("the server certificate (fingerprint %s) does not match the trusted fingerprint", fp)
				}
				return nil
			}
			pool := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				pool.AddCert(c)
			}
			if _, err := leaf.Verify(x509.VerifyOptions{DNSName: l.Host, Intermediates: pool}); err != nil {
				return fmt.Errorf("the server certificate is not trusted (%v); to trust it anyway, enter its fingerprint %s", err, fp)
			}
			return nil
		},
	}
}

// CertFingerprint is the SHA256 fingerprint of a certificate, as hex pairs.
func CertFingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return formatFingerprint(sum[:])
}

func formatFingerprint(b []byte) string {
	h := strings.ToUpper(hex.EncodeToString(b))
	var parts []string
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// normalizeFingerprint accepts fingerprints with or without colons, in any
// case, with an optional "SHA256:" prefix.
func normalizeFingerprint(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "SHA256:"), "sha256:")
	s = strings.NewReplacer(":", "", " ", "").Replace(s)
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != sha256.Size {
		return strings.TrimSpace(s)
	}
	return formatFingerprint(b)
}

// watch closes the connection when ctx ends, which aborts a transfer that
// hangs; the returned function stops watching.
func (t *ftpTarget) watch(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			t.c.Quit()
		case <-done:
		}
	}()
	return func() { close(done) }
}

func (t *ftpTarget) Upload(ctx context.Context, name string, r io.Reader, size int64) error {
	defer t.watch(ctx)()
	part := name + ".part"
	if err := t.c.Stor(part, r); err != nil {
		return fmt.Errorf("uploading %s: %w", name, ctxErr(ctx, err))
	}
	if err := t.c.Rename(part, name); err != nil {
		t.c.Delete(part)
		return fmt.Errorf("renaming %s: %w", part, err)
	}
	return nil
}

func (t *ftpTarget) Download(ctx context.Context, name string, w io.Writer) error {
	defer t.watch(ctx)()
	resp, err := t.c.Retr(name)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", name, ctxErr(ctx, err))
	}
	_, err = io.Copy(w, resp)
	if cerr := resp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("downloading %s: %w", name, ctxErr(ctx, err))
	}
	return nil
}

func (t *ftpTarget) List(ctx context.Context) ([]File, error) {
	defer t.watch(ctx)()
	entries, err := t.c.List("")
	if err != nil {
		return nil, fmt.Errorf("listing the directory: %w", ctxErr(ctx, err))
	}
	var out []File
	for _, e := range entries {
		if e.Type == ftp.EntryTypeFile {
			out = append(out, File{Name: path.Base(e.Name), Size: int64(e.Size)})
		}
	}
	return out, nil
}

func (t *ftpTarget) Delete(ctx context.Context, name string) error {
	defer t.watch(ctx)()
	if err := t.c.Delete(name); err != nil {
		return fmt.Errorf("deleting %s: %w", name, err)
	}
	return nil
}

func (t *ftpTarget) Close() error { return t.c.Quit() }

// ctxErr prefers the reason a context ended over the error it caused.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
