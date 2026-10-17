// Package mail sends email through the SMTP server an admin configures, for
// example to hand users their login details or an invitation link.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Security is how the connection to the mail server is protected.
type Security string

const (
	// StartTLS connects in plain text and upgrades with STARTTLS (port 587).
	StartTLS Security = "starttls"
	// TLS connects with TLS right away (port 465).
	TLS Security = "tls"
	// None sends without encryption; only sensible for a local relay.
	None Security = "none"
)

// Config describes the SMTP server mail is sent through.
type Config struct {
	Host     string
	Port     int
	Security Security
	Username string // empty = no authentication
	Password string
	From     string // sender address
	FromName string // optional display name of the sender
}

// Configured reports whether enough is set to send mail.
func (c Config) Configured() bool {
	return c.Host != "" && c.From != ""
}

// Validate checks and normalises the configuration. An empty host means
// mail is switched off; the other fields are then not checked.
func (c *Config) Validate() error {
	c.Host = strings.TrimSpace(c.Host)
	c.Username = strings.TrimSpace(c.Username)
	c.From = strings.TrimSpace(c.From)
	c.FromName = strings.TrimSpace(c.FromName)
	switch c.Security {
	case StartTLS, TLS, None:
	case "":
		c.Security = StartTLS
	default:
		return fmt.Errorf("unknown mail security %q", c.Security)
	}
	if c.Port == 0 {
		c.Port = DefaultPort(c.Security)
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("mail server port %d is invalid (1-65535)", c.Port)
	}
	if c.Host == "" {
		return nil
	}
	if strings.ContainsAny(c.Host, " /:\r\n") {
		return errors.New("mail server must be a host name or IP address, without port or scheme")
	}
	if c.From == "" {
		return errors.New("enter the sender address mail is sent from")
	}
	if _, err := ParseAddress(c.From); err != nil {
		return errors.New("sender address is not a valid email address")
	}
	if strings.ContainsAny(c.FromName, "\r\n") {
		return errors.New("sender name must be a single line")
	}
	return nil
}

// DefaultPort is the usual port for a security mode.
func DefaultPort(s Security) int {
	switch s {
	case TLS:
		return 465
	case None:
		return 25
	}
	return 587
}

// ParseAddress checks a single bare email address such as
// "jan@example.com" and returns it trimmed.
func ParseAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" {
		return "", errors.New("not a valid email address")
	}
	return a.Address, nil
}

// Message is a plain text email to one recipient.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Timeout bounds a whole delivery.
const Timeout = 20 * time.Second

// Send delivers msg through the server in c.
func Send(ctx context.Context, c Config, msg Message) error {
	if !c.Configured() {
		return errors.New("no mail server is configured")
	}
	to, err := ParseAddress(msg.To)
	if err != nil {
		return fmt.Errorf("recipient %q is %w", msg.To, err)
	}
	data, err := build(c, to, msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	tlsConf := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	if c.Security == TLS {
		d := tls.Dialer{Config: tlsConf}
		conn, err = d.DialContext(ctx, "tcp", addr)
	} else {
		var d net.Dialer
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connecting to mail server: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail server greeting: %w", err)
	}
	defer cl.Close()
	if err := cl.Hello(helloName(c.From)); err != nil {
		return fmt.Errorf("mail server: %w", err)
	}
	if c.Security == StartTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("mail server does not offer STARTTLS; choose another security setting")
		}
		if err := cl.StartTLS(tlsConf); err != nil {
			return fmt.Errorf("starting TLS: %w", err)
		}
	}
	if c.Username != "" {
		if ok, _ := cl.Extension("AUTH"); !ok {
			return errors.New("mail server does not accept a login; clear the username or choose another security setting")
		}
		if err := cl.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); err != nil {
			return fmt.Errorf("logging in to mail server: %w", err)
		}
	}
	from, _ := ParseAddress(c.From)
	if err := cl.Mail(from); err != nil {
		return fmt.Errorf("mail server refused the sender: %w", err)
	}
	if err := cl.Rcpt(to); err != nil {
		return fmt.Errorf("mail server refused the recipient: %w", err)
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("mail server: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("sending mail: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail server refused the message: %w", err)
	}
	return cl.Quit()
}

// helloName is the name given in EHLO: the domain of the sender address,
// which servers accept more readily than "localhost".
func helloName(from string) string {
	if i := strings.LastIndexByte(from, '@'); i >= 0 && i < len(from)-1 {
		return from[i+1:]
	}
	return "localhost"
}

// build renders the message with headers.
func build(c Config, to string, msg Message) ([]byte, error) {
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return nil, errors.New("subject must be a single line")
	}
	from := mail.Address{Name: c.FromName, Address: c.From}
	var b bytes.Buffer
	hdr := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	hdr("From", from.String())
	hdr("To", to)
	hdr("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	hdr("Date", time.Now().Format(time.RFC1123Z))
	hdr("Message-ID", "<"+randomID()+"@"+helloName(c.From)+">")
	hdr("MIME-Version", "1.0")
	hdr("Content-Type", "text/plain; charset=utf-8")
	hdr("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	body := strings.ReplaceAll(msg.Body, "\r\n", "\n")
	if _, err := qp.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func randomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
