package backup

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"
)

type ftpDriver struct {
	fs   afero.Fs
	cert tls.Certificate
	mode ftpserver.TLSRequirement
	addr string
}

func (d *ftpDriver) GetSettings() (*ftpserver.Settings, error) {
	return &ftpserver.Settings{ListenAddr: d.addr, TLSRequired: d.mode}, nil
}
func (d *ftpDriver) ClientConnected(ftpserver.ClientContext) (string, error) { return "test", nil }
func (d *ftpDriver) ClientDisconnected(ftpserver.ClientContext)              {}
func (d *ftpDriver) AuthUser(_ ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	if user != "backup" || pass != "secret" {
		return nil, errors.New("bad login")
	}
	return d.fs, nil
}
func (d *ftpDriver) GetTLSConfig() (*tls.Config, error) {
	return &tls.Config{Certificates: []tls.Certificate{d.cert}}, nil
}

func selfSigned(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, c
}

func startFTP(t *testing.T, mode ftpserver.TLSRequirement) (Location, *x509.Certificate) {
	t.Helper()
	cert, leaf := selfSigned(t)
	d := &ftpDriver{fs: afero.NewMemMapFs(), cert: cert, mode: mode, addr: "127.0.0.1:0"}
	srv := ftpserver.NewFtpServer(d)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Stop() })
	host, port, _ := net.SplitHostPort(srv.Addr())
	p, _ := strconv.Atoi(port)
	return Location{Host: host, Port: p, Username: "backup", Password: "secret", Dir: "/a/b"}, leaf
}

func exercise(t *testing.T, l Location) {
	t.Helper()
	ctx := context.Background()
	if _, err := Test(ctx, l); err != nil {
		t.Fatal("test:", err)
	}
	tg, err := dial(ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	body := strings.Repeat("backup data ", 10000)
	if err := tg.Upload(ctx, "f.tar.gz", strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	files, err := tg.List(ctx)
	if err != nil || len(files) != 1 || files[0].Name != "f.tar.gz" || files[0].Size != int64(len(body)) {
		t.Fatalf("list = %+v, %v", files, err)
	}
	var b strings.Builder
	if err := tg.Download(ctx, "f.tar.gz", &b); err != nil || b.String() != body {
		t.Fatalf("download: %v (got %d bytes)", err, b.Len())
	}
	if err := tg.Delete(ctx, "f.tar.gz"); err != nil {
		t.Fatal(err)
	}
	if files, _ := tg.List(ctx); len(files) != 0 {
		t.Fatalf("after delete = %+v", files)
	}
}

func TestFTP(t *testing.T) {
	l, _ := startFTP(t, ftpserver.ClearOrEncrypted)
	l.Protocol = FTP
	exercise(t, l)
	l.Password = "wrong"
	if _, err := Test(context.Background(), l); err == nil {
		t.Fatal("wrong password accepted")
	}
}

func TestFTPS(t *testing.T) {
	for _, mode := range []struct {
		proto string
		tls   ftpserver.TLSRequirement
	}{{FTPS, ftpserver.MandatoryEncryption}, {FTPSImplicit, ftpserver.ImplicitEncryption}} {
		t.Run(mode.proto, func(t *testing.T) {
			l, leaf := startFTP(t, mode.tls)
			l.Protocol = mode.proto
			// The self-signed certificate is refused, and the error names
			// its fingerprint.
			_, err := Test(context.Background(), l)
			if err == nil || !strings.Contains(err.Error(), CertFingerprint(leaf)) {
				t.Fatalf("untrusted certificate: %v", err)
			}
			l.Fingerprint = strings.Repeat("00:", 31) + "00"
			if _, err := Test(context.Background(), l); err == nil {
				t.Fatal("wrong fingerprint accepted")
			}
			l.Fingerprint = CertFingerprint(leaf)
			exercise(t, l)
		})
	}
}
