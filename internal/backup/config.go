// Package backup makes full and incremental backups of the portal database
// to a remote location over FTP, FTPS or SCP, and restores them from there.
//
// A full backup holds the whole database file. An incremental backup holds
// only the blocks of the file that changed since the previous backup, so a
// restore applies the last full backup and then every incremental backup
// after it, in order.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// Protocols a remote location can use.
const (
	FTP          = "ftp"           // plain FTP, unencrypted
	FTPS         = "ftps"          // FTP with explicit TLS (AUTH TLS), usually port 21
	FTPSImplicit = "ftps-implicit" // FTP with implicit TLS, usually port 990
	SCP          = "scp"           // SCP over SSH, usually port 22
)

// Schedules for automatic backups.
const (
	Off     = "off"
	Hourly  = "hourly"
	Every6  = "6h"
	Every12 = "12h"
	Daily   = "daily"
	Weekly  = "weekly"
)

// Location is a remote place to keep backups.
type Location struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"` // 0 means the protocol's default
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	// Dir is the remote directory, relative to the login directory unless
	// it starts with "/". It is created when missing.
	Dir string `json:"dir"`
	// PrivateKey is an OpenSSH private key for SCP. When it is protected,
	// Password is used as its passphrase.
	PrivateKey string `json:"private_key,omitempty"`
	// HostKey is the SHA256 fingerprint of the SSH server's host key. It is
	// filled in by the first connection test and checked from then on.
	HostKey string `json:"host_key,omitempty"`
	// Fingerprint is the SHA256 fingerprint of an FTPS server certificate
	// to trust even when no known authority signed it (self-signed).
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Configured reports whether a location has been entered.
func (l Location) Configured() bool { return strings.TrimSpace(l.Host) != "" }

// DefaultPort is the usual port of the protocol.
func (l Location) DefaultPort() int {
	switch l.Protocol {
	case SCP:
		return 22
	case FTPSImplicit:
		return 990
	}
	return 21
}

func (l Location) port() int {
	if l.Port == 0 {
		return l.DefaultPort()
	}
	return l.Port
}

func (l Location) addr() string { return net.JoinHostPort(l.Host, strconv.Itoa(l.port())) }

// ProtocolName is the protocol as shown to people.
func (l Location) ProtocolName() string {
	switch l.Protocol {
	case FTP:
		return "FTP"
	case FTPS:
		return "FTPS (explicit TLS)"
	case FTPSImplicit:
		return "FTPS (implicit TLS)"
	case SCP:
		return "SCP (SSH)"
	}
	return l.Protocol
}

// String identifies the location, without secrets, for example
// "scp://backup@nas.local:22/iptv".
func (l Location) String() string {
	user := ""
	if l.Username != "" {
		user = l.Username + "@"
	}
	dir := l.Dir
	if !strings.HasPrefix(dir, "/") {
		dir = "/" + dir
	}
	return l.Protocol + "://" + user + l.addr() + dir
}

// Validate checks and normalises the location.
func (l *Location) Validate() error {
	l.Host = strings.TrimSpace(l.Host)
	l.Username = strings.TrimSpace(l.Username)
	l.Dir = strings.TrimSpace(l.Dir)
	l.HostKey = strings.TrimSpace(l.HostKey)
	l.Fingerprint = normalizeFingerprint(l.Fingerprint)
	l.PrivateKey = strings.TrimSpace(l.PrivateKey)
	switch l.Protocol {
	case FTP, FTPS, FTPSImplicit, SCP:
	default:
		return fmt.Errorf("protocol %q is not supported (ftp, ftps, ftps-implicit or scp)", l.Protocol)
	}
	if l.Host == "" {
		return errors.New("enter the server")
	}
	if strings.ContainsAny(l.Host, " /\\") {
		return fmt.Errorf("server %q is invalid; enter a host name or IP address without a path", l.Host)
	}
	if l.Port < 0 || l.Port > 65535 {
		return fmt.Errorf("port %d is invalid (1-65535)", l.Port)
	}
	if strings.ContainsAny(l.Dir, "\x00\r\n'") {
		return errors.New("the directory may not contain quotes or line breaks")
	}
	if l.Dir != "/" {
		l.Dir = strings.TrimRight(l.Dir, "/")
	}
	if strings.HasPrefix(l.Dir, "-") {
		l.Dir = "./" + l.Dir
	}
	if l.Protocol == SCP && l.Username == "" {
		return errors.New("SCP needs a username")
	}
	if l.Protocol == SCP && l.Password == "" && l.PrivateKey == "" {
		return errors.New("SCP needs a password or a private key")
	}
	if l.PrivateKey != "" {
		if _, err := parsePrivateKey(l.PrivateKey, l.Password); err != nil {
			return err
		}
	}
	return nil
}

// Config is the backup configuration, kept in the database.
type Config struct {
	Location Location `json:"location"`
	// Schedule is how often a backup runs automatically.
	Schedule string `json:"schedule"`
	// Hour is the hour of the day (0-23, local time) daily and weekly
	// backups run at. Weekly backups run on Sunday.
	Hour int `json:"hour"`
	// FullEvery is the number of days after which the next backup is a
	// full one; the backups in between are incremental.
	FullEvery int `json:"full_every"`
	// Keep is the number of full backups, each with its incremental
	// backups, kept on the remote location. Older ones are deleted.
	Keep int `json:"keep"`
	// Passphrase encrypts backups (age, scrypt) when set.
	Passphrase string `json:"passphrase,omitempty"`
}

// DefaultConfig is used until an admin saves a configuration.
var DefaultConfig = Config{
	Location:  Location{Protocol: SCP},
	Schedule:  Off,
	Hour:      3,
	FullEvery: 7,
	Keep:      4,
}

// Validate checks and normalises the schedule part of the configuration.
func (c *Config) Validate() error {
	switch c.Schedule {
	case Off, Hourly, Every6, Every12, Daily, Weekly:
	case "":
		c.Schedule = Off
	default:
		return fmt.Errorf("schedule %q is invalid", c.Schedule)
	}
	if c.Hour < 0 || c.Hour > 23 {
		return errors.New("the hour must be between 0 and 23")
	}
	if c.FullEvery < 1 || c.FullEvery > 365 {
		return errors.New("make a full backup every 1 to 365 days")
	}
	if c.Keep < 1 || c.Keep > 100 {
		return errors.New("keep 1 to 100 full backups")
	}
	if c.Schedule != Off && !c.Location.Configured() {
		return errors.New("set up a remote location before switching on automatic backups")
	}
	return nil
}

// Next is when the next automatic backup is due after a backup attempt at
// last. It is the zero time when automatic backups are off. A zero last
// time makes a backup due right away.
func (c Config) Next(last time.Time) time.Time {
	if last.IsZero() && c.Schedule != Off {
		return last
	}
	switch c.Schedule {
	case Hourly:
		return last.Add(time.Hour)
	case Every6:
		return last.Add(6 * time.Hour)
	case Every12:
		return last.Add(12 * time.Hour)
	case Daily, Weekly:
		l := last.Local()
		t := time.Date(l.Year(), l.Month(), l.Day(), c.Hour, 0, 0, 0, time.Local)
		for !t.After(last) || (c.Schedule == Weekly && t.Weekday() != time.Sunday) {
			t = t.AddDate(0, 0, 1)
		}
		return t
	}
	return time.Time{}
}

// ScheduleName describes the schedule, for example "daily at 03:00".
func (c Config) ScheduleName() string {
	at := fmt.Sprintf("%02d:00", c.Hour)
	switch c.Schedule {
	case Hourly:
		return "every hour"
	case Every6:
		return "every 6 hours"
	case Every12:
		return "every 12 hours"
	case Daily:
		return "daily at " + at
	case Weekly:
		return "every Sunday at " + at
	}
	return "off"
}

const configKey = "backup_config"

// LoadConfig reads the configuration from the database.
func LoadConfig(ctx context.Context, st *store.Store) (Config, error) {
	c := DefaultConfig
	v, err := st.Value(ctx, configKey)
	if errors.Is(err, store.ErrNotFound) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(v), &c); err != nil {
		return DefaultConfig, fmt.Errorf("reading the backup settings: %w", err)
	}
	return c, nil
}

// SaveConfig validates and stores the configuration.
func SaveConfig(ctx context.Context, st *store.Store, c Config) error {
	if c.Location.Configured() {
		if err := c.Location.Validate(); err != nil {
			return err
		}
	}
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return st.SetValue(ctx, configKey, string(b))
}
