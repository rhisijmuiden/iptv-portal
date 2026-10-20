package backup

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startSSH runs an SSH server that speaks just enough scp, and runs other
// commands with sh, in a temporary directory.
func startSSH(t *testing.T, authorized ssh.PublicKey) (Location, string) {
	t.Helper()
	root := t.TempDir()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ := ssh.NewSignerFromKey(hostPriv)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == "backup" && string(pw) == "secret" {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if authorized != nil && string(k.Marshal()) == string(authorized.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	cfg.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSH(c, cfg, root)
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return Location{Protocol: SCP, Host: host, Port: p, Username: "backup", Dir: "backups/iptv",
		HostKey: ssh.FingerprintSHA256(hostKey.PublicKey())}, root
}

func serveSSH(c net.Conn, cfg *ssh.ServerConfig, root string) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, reqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range reqs {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				cmd := string(req.Payload[4:])
				req.Reply(true, nil)
				status := runCommand(ch, root, cmd)
				ch.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, uint32(status)))
				return
			}
		}()
	}
}

// unquote resolves shell quoting with the shell itself.
func unquote(root, s string) string {
	out, _ := exec.Command("sh", "-c", "printf %s "+s).Output()
	p := string(out)
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	return p
}

func runCommand(ch ssh.Channel, root, cmd string) int {
	r := bufio.NewReader(ch)
	fail := func(err error) int {
		fmt.Fprintf(ch.Stderr(), "%v\n", err)
		ch.Write([]byte("\x01" + err.Error() + "\n"))
		return 1
	}
	switch {
	case strings.HasPrefix(cmd, "scp -t "):
		dir := unquote(root, strings.TrimPrefix(cmd, "scp -t "))
		ch.Write([]byte{0})
		line, _ := r.ReadString('\n')
		var mode string
		var size int64
		var name string
		fmt.Sscanf(line, "C%s %d %s", &mode, &size, &name)
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			return fail(err)
		}
		ch.Write([]byte{0})
		io.CopyN(f, r, size)
		f.Close()
		r.ReadByte()
		ch.Write([]byte{0})
		return 0
	case strings.HasPrefix(cmd, "scp -f "):
		p := unquote(root, strings.TrimPrefix(cmd, "scp -f "))
		r.ReadByte()
		b, err := os.ReadFile(p)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(ch, "C0600 %d %s\n", len(b), filepath.Base(p))
		r.ReadByte()
		ch.Write(b)
		ch.Write([]byte{0})
		r.ReadByte()
		return 0
	}
	sh := exec.Command("sh", "-c", cmd)
	sh.Dir = root
	sh.Stdout, sh.Stderr = ch, ch.Stderr()
	if err := sh.Run(); err != nil {
		return 1
	}
	return 0
}

func TestSCP(t *testing.T) {
	priv, pub, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	authorized, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		t.Fatal(err)
	}
	l, root := startSSH(t, authorized)
	ctx := context.Background()

	t.Run("password", func(t *testing.T) {
		l := l
		l.Password = "secret"
		exercise(t, l)
		if _, err := os.Stat(filepath.Join(root, "backups/iptv")); err != nil {
			t.Fatal("directory not created:", err)
		}
	})
	t.Run("key", func(t *testing.T) {
		l := l
		l.PrivateKey = priv
		exercise(t, l)
	})
	t.Run("host key", func(t *testing.T) {
		l := l
		l.Password = "secret"
		want := l.HostKey
		l.HostKey = ""
		if _, err := dial(ctx, l); err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("unknown host key: %v", err)
		}
		got, err := Test(ctx, l)
		if err != nil || got != want {
			t.Fatalf("Test = %q, %v; want %q", got, err, want)
		}
		l.HostKey = "SHA256:somethingelse"
		if _, err := dial(ctx, l); err == nil || !strings.Contains(err.Error(), "changed") {
			t.Fatalf("changed host key: %v", err)
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		l := l
		l.Password = "nope"
		if _, err := dial(ctx, l); err == nil {
			t.Fatal("wrong password accepted")
		}
	})
}

func TestServiceOverSCP(t *testing.T) {
	ctx := context.Background()
	l, root := startSSH(t, nil)
	l.Password = "secret"
	st, s, now := setup(t, "")
	c, _ := LoadConfig(ctx, st)
	c.Location = l
	if err := SaveConfig(ctx, st, c); err != nil {
		t.Fatal(err)
	}
	addCategories(t, st, "a", 50)
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	addCategories(t, st, "b", 2)
	inc, err := s.Run(ctx, false)
	if err != nil || inc.Kind != Incremental {
		t.Fatalf("incremental = %+v, %v", inc, err)
	}
	files, err := s.Remote(ctx)
	if err != nil || len(files) != 2 {
		t.Fatalf("remote = %+v, %v", files, err)
	}
	if es, _ := os.ReadDir(filepath.Join(root, "backups/iptv")); len(es) != 2 {
		t.Fatalf("remote directory holds %d files", len(es))
	}
	if _, err := s.Restore(ctx, inc.Name); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if n := categoryCount(t, st.Dir()); n != 52 {
		t.Fatalf("restored %d categories, want 52", n)
	}
}
