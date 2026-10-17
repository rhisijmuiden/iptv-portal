package mail

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"mime/quotedprintable"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeServer is a minimal SMTP server that records one session.
type fakeServer struct {
	addr     string
	auth     chan string
	commands chan []string
	data     chan string
}

func startFake(t *testing.T) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeServer{addr: ln.Addr().String(), auth: make(chan string, 1),
		commands: make(chan []string, 1), data: make(chan string, 1)}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		say := func(s string) { io.WriteString(c, s+"\r\n") }
		say("220 fake ready")
		var cmds []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
			cmds = append(cmds, verb)
			switch verb {
			case "EHLO":
				say("250-fake")
				say("250 AUTH PLAIN")
			case "AUTH":
				raw, _ := base64.StdEncoding.DecodeString(strings.Fields(line)[2])
				f.auth <- string(raw)
				say("235 ok")
			case "MAIL", "RCPT":
				say("250 ok")
			case "DATA":
				say("354 go")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				f.data <- b.String()
				say("250 queued")
			case "QUIT":
				say("221 bye")
				f.commands <- cmds
				return
			default:
				say("502 unknown")
			}
		}
	}()
	return f
}

func TestSend(t *testing.T) {
	f := startFake(t)
	host, port, _ := net.SplitHostPort(f.addr)
	c := Config{Host: host, Port: atoi(port), Security: None, Username: "admin", Password: "s3cret",
		From: "tv@example.com", FromName: "Télé Portal"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	exp := time.Date(2027, 3, 1, 12, 0, 0, 0, time.Local)
	msg := LoginMessage("jan@example.org", Login{PortalName: "My TV", PortalURL: "https://tv.example.com",
		Username: "jan", Password: "pa$$w0rd", ExpiresAt: &exp})
	if err := Send(context.Background(), c, msg); err != nil {
		t.Fatal(err)
	}
	if got := <-f.auth; got != "\x00admin\x00s3cret" {
		t.Errorf("auth = %q", got)
	}
	data := <-f.data
	head, body, _ := strings.Cut(data, "\r\n\r\n")
	for _, want := range []string{"To: jan@example.org", "Subject: Your login details for My TV", "From: =?utf-8?q?T=C3=A9l=C3=A9_Portal?= <tv@example.com>"} {
		if !strings.Contains(head, want) {
			t.Errorf("headers lack %q:\n%s", want, head)
		}
	}
	plain, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Password:            pa$$w0rd", "https://tv.example.com/get.php?username=jan&password=pa%24%24w0rd", "1 March 2027"} {
		if !strings.Contains(string(plain), want) {
			t.Errorf("body lacks %q:\n%s", want, plain)
		}
	}
	if cmds := <-f.commands; strings.Join(cmds, " ") != "EHLO AUTH MAIL RCPT DATA QUIT" {
		t.Errorf("commands = %v", cmds)
	}
}

func TestSendRequiresStartTLS(t *testing.T) {
	f := startFake(t)
	host, port, _ := net.SplitHostPort(f.addr)
	c := Config{Host: host, Security: StartTLS, From: "tv@example.com"}
	c.Port = atoi(port)
	err := Send(context.Background(), c, TestMessage("a@example.com", "TV"))
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v, want STARTTLS error", err)
	}
}

func atoi(s string) int {
	n := 0
	for _, ch := range s {
		n = n*10 + int(ch-'0')
	}
	return n
}

func TestValidate(t *testing.T) {
	cases := []struct {
		c  Config
		ok bool
	}{
		{Config{}, true}, // mail off
		{Config{Host: "smtp.example.com", From: "tv@example.com"}, true},
		{Config{Host: "smtp.example.com"}, false},
		{Config{Host: "smtp.example.com:587", From: "tv@example.com"}, false},
		{Config{Host: "smtp.example.com", From: "TV <tv@example.com>"}, false},
		{Config{Host: "smtp.example.com", From: "tv@example.com", Security: "ssl"}, false},
	}
	for _, tc := range cases {
		c := tc.c
		if err := c.Validate(); (err == nil) != tc.ok {
			t.Errorf("%+v: err = %v", tc.c, err)
		}
	}
	c := Config{Host: "smtp.example.com", From: "tv@example.com", Security: TLS}
	c.Validate()
	if c.Port != 465 {
		t.Errorf("default TLS port = %d", c.Port)
	}
}

func TestRejectsHeaderInjection(t *testing.T) {
	c := Config{Host: "127.0.0.1", Port: 1, From: "tv@example.com"}
	if err := Send(context.Background(), c, Message{To: "a@example.com\r\nBcc: x@example.com", Subject: "x"}); err == nil {
		t.Fatal("accepted a recipient with a line break")
	}
	if err := Send(context.Background(), c, Message{To: "a@example.com", Subject: "x\r\nBcc: x@example.com"}); err == nil {
		t.Fatal("accepted a subject with a line break")
	}
}
