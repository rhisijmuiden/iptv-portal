package backup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

type scpTarget struct {
	c       *ssh.Client
	dir     string // remote directory, "." for the login directory
	hostKey string // fingerprint of the server's host key
}

func dialSCP(ctx context.Context, l Location, acceptNewKey bool) (*scpTarget, error) {
	t := &scpTarget{dir: l.Dir}
	if t.dir == "" {
		t.dir = "."
	}
	cfg := &ssh.ClientConfig{
		User:    l.Username,
		Timeout: dialTimeout,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			t.hostKey = ssh.FingerprintSHA256(key)
			switch {
			case l.HostKey == "" && acceptNewKey:
				return nil
			case l.HostKey == "":
				return fmt.Errorf("the host key of the server is unknown (%s); test the connection to trust it", t.hostKey)
			case l.HostKey != t.hostKey:
				return fmt.Errorf("the host key of the server changed: expected %s, got %s; if the server was reinstalled, clear the stored host key and test the connection again", l.HostKey, t.hostKey)
			}
			return nil
		},
	}
	if l.PrivateKey != "" {
		signer, err := parsePrivateKey(l.PrivateKey, l.Password)
		if err != nil {
			return nil, err
		}
		cfg.Auth = append(cfg.Auth, ssh.PublicKeys(signer))
	}
	if l.Password != "" && l.PrivateKey == "" {
		pw := l.Password
		cfg.Auth = append(cfg.Auth, ssh.Password(pw),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = pw
				}
				return answers, nil
			}))
	}
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", l.addr())
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", l.addr(), err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, l.addr(), cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("connecting to %s: %w", l.addr(), err)
	}
	t.c = ssh.NewClient(cc, chans, reqs)
	if t.dir != "." {
		if _, err := t.run(ctx, "mkdir -p "+quote(t.dir), nil); err != nil {
			t.c.Close()
			return nil, fmt.Errorf("creating directory %s: %w", t.dir, err)
		}
	}
	return t, nil
}

func parsePrivateKey(key, passphrase string) (ssh.Signer, error) {
	signer, err := ssh.ParsePrivateKey([]byte(key))
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		if passphrase == "" {
			return nil, errors.New("the private key is protected; enter its passphrase as the password")
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(key), []byte(passphrase))
	}
	if err != nil {
		return nil, fmt.Errorf("the private key cannot be read: %w", err)
	}
	return signer, nil
}

// GenerateKey creates an Ed25519 key pair: the private key in OpenSSH
// format and the public key as a line for authorized_keys.
func GenerateKey() (private, public string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "iptv-portal-backup")
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(block)), authorizedKey(sshPub), nil
}

// PublicKey returns the authorized_keys line of a private key.
func PublicKey(private, passphrase string) (string, error) {
	signer, err := parsePrivateKey(private, passphrase)
	if err != nil {
		return "", err
	}
	return authorizedKey(signer.PublicKey()), nil
}

func authorizedKey(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) + " iptv-portal-backup"
}

// quote makes s a single shell word.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// run executes a command on the server and returns its output.
func (t *scpTarget) run(ctx context.Context, cmd string, stdin io.Reader) ([]byte, error) {
	s, err := t.c.NewSession()
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var out, stderr bytes.Buffer
	s.Stdout, s.Stderr, s.Stdin = &out, &stderr, stdin
	done := make(chan error, 1)
	go func() { done <- s.Run(cmd) }()
	select {
	case <-ctx.Done():
		s.Close()
		return nil, ctx.Err()
	case err = <-done:
	}
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return out.Bytes(), nil
}

func (t *scpTarget) file(name string) string { return path.Join(t.dir, name) }

// session starts a command whose input and output the caller drives.
func (t *scpTarget) session(ctx context.Context, cmd string) (*ssh.Session, io.WriteCloser, *bufio.Reader, func(), error) {
	s, err := t.c.NewSession()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	in, err := s.StdinPipe()
	if err != nil {
		s.Close()
		return nil, nil, nil, nil, err
	}
	outPipe, err := s.StdoutPipe()
	if err != nil {
		s.Close()
		return nil, nil, nil, nil, err
	}
	var stderr bytes.Buffer
	s.Stderr = &stderr
	if err := s.Start(cmd); err != nil {
		s.Close()
		return nil, nil, nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { s.Close() })
	return s, in, bufio.NewReader(outPipe), func() { stop(); s.Close() }, nil
}

// ack reads the reply of the scp program: 0 is OK, 1 and 2 carry an error.
func ack(r *bufio.Reader) error {
	b, err := r.ReadByte()
	if err != nil {
		return fmt.Errorf("scp: %w", err)
	}
	if b == 0 {
		return nil
	}
	msg, _ := r.ReadString('\n')
	if b == 1 || b == 2 {
		return fmt.Errorf("scp: %s", strings.TrimSpace(msg))
	}
	return fmt.Errorf("scp: unexpected reply %q", string(b)+msg)
}

func (t *scpTarget) Upload(ctx context.Context, name string, r io.Reader, size int64) error {
	part := name + ".part"
	if err := t.send(ctx, part, r, size); err != nil {
		return fmt.Errorf("uploading %s: %w", name, ctxErr(ctx, err))
	}
	if _, err := t.run(ctx, "mv -f "+quote(t.file(part))+" "+quote(t.file(name)), nil); err != nil {
		t.Delete(ctx, part)
		return fmt.Errorf("renaming %s: %w", part, err)
	}
	return nil
}

// send uploads one file with the scp protocol ("scp -t" on the server).
func (t *scpTarget) send(ctx context.Context, name string, r io.Reader, size int64) error {
	s, in, out, closeFn, err := t.session(ctx, "scp -t "+quote(t.dir))
	if err != nil {
		return err
	}
	defer closeFn()
	if err := ack(out); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(in, "C0600 %d %s\n", size, name); err != nil {
		return err
	}
	if err := ack(out); err != nil {
		return err
	}
	if n, err := io.CopyN(in, r, size); err != nil {
		return fmt.Errorf("sent %d of %d bytes: %w", n, size, err)
	}
	if _, err := in.Write([]byte{0}); err != nil {
		return err
	}
	if err := ack(out); err != nil {
		return err
	}
	in.Close()
	return s.Wait()
}

func (t *scpTarget) Download(ctx context.Context, name string, w io.Writer) error {
	if err := t.receive(ctx, name, w); err != nil {
		return fmt.Errorf("downloading %s: %w", name, ctxErr(ctx, err))
	}
	return nil
}

// receive downloads one file with the scp protocol ("scp -f" on the server).
func (t *scpTarget) receive(ctx context.Context, name string, w io.Writer) error {
	s, in, out, closeFn, err := t.session(ctx, "scp -f "+quote(t.file(name)))
	if err != nil {
		return err
	}
	defer closeFn()
	if _, err := in.Write([]byte{0}); err != nil {
		return err
	}
	for {
		b, err := out.ReadByte()
		if err != nil {
			return fmt.Errorf("scp: %w", err)
		}
		line, err := out.ReadString('\n')
		if err != nil {
			return fmt.Errorf("scp: %w", err)
		}
		switch b {
		case 1, 2:
			return fmt.Errorf("scp: %s", strings.TrimSpace(line))
		case 'T': // modification times; acknowledge and wait for the file
			if _, err := in.Write([]byte{0}); err != nil {
				return err
			}
			continue
		case 'C':
		default:
			return fmt.Errorf("scp: unexpected reply %q", string(b)+line)
		}
		fields := strings.Fields(line) // mode, size, name
		if len(fields) < 3 {
			return fmt.Errorf("scp: invalid header %q", line)
		}
		size, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || size < 0 {
			return fmt.Errorf("scp: invalid size in %q", line)
		}
		if _, err := in.Write([]byte{0}); err != nil {
			return err
		}
		if _, err := io.CopyN(w, out, size); err != nil {
			return err
		}
		if err := ack(out); err != nil {
			return err
		}
		if _, err := in.Write([]byte{0}); err != nil {
			return err
		}
		in.Close()
		return s.Wait()
	}
}

func (t *scpTarget) List(ctx context.Context) ([]File, error) {
	out, err := t.run(ctx, "LC_ALL=C ls -ln "+quote(t.dir), nil)
	if err != nil {
		return nil, fmt.Errorf("listing the directory: %w", err)
	}
	var files []File
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 9 || !strings.HasPrefix(f[0], "-") {
			continue
		}
		size, err := strconv.ParseInt(f[4], 10, 64)
		if err != nil {
			continue
		}
		// Backup names contain no spaces, so the last field is the name.
		files = append(files, File{Name: f[len(f)-1], Size: size})
	}
	return files, nil
}

func (t *scpTarget) Delete(ctx context.Context, name string) error {
	if _, err := t.run(ctx, "rm -f "+quote(t.file(name)), nil); err != nil {
		return fmt.Errorf("deleting %s: %w", name, err)
	}
	return nil
}

func (t *scpTarget) Close() error { return t.c.Close() }
