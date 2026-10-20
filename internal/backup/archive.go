package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"time"

	"filippo.io/age"
)

// Kinds of backup.
const (
	Full        = "full"
	Incremental = "incremental"
)

// BlockSize is the unit an incremental backup compares the database in.
const BlockSize = 32 << 10

const (
	formatVersion = 1
	manifestEntry = "manifest.json"
	databaseEntry = "portal.db" // full backups: the whole file
	blocksEntry   = "blocks"    // incremental backups: the changed blocks
	namePrefix    = "iptv-portal-"
	timeLayout    = "20060102-150405"
	encryptedExt  = ".age"
)

var namePattern = regexp.MustCompile(`^iptv-portal-(\d{8}-\d{6})-(full|incr)\.tar\.gz(\.age)?$`)

// Manifest describes one backup. It is the first entry of every archive.
type Manifest struct {
	Format  int       `json:"format"`
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Created time.Time `json:"created"`
	// Parent is the file name of the backup an incremental backup builds on.
	Parent string `json:"parent,omitempty"`
	// Version is the IPTV Portal version that made the backup.
	Version string `json:"version"`
	// Size and SHA256 describe the database file the backup restores to.
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// BlockSize and Blocks (the SHA-256 of every block) let the next
	// incremental backup find what changed.
	BlockSize int      `json:"block_size"`
	Blocks    []string `json:"blocks"`
	// Changed lists the blocks an incremental backup holds, in order.
	Changed []int `json:"changed,omitempty"`
}

// File is a backup on the remote location.
type File struct {
	Name      string
	Size      int64
	Kind      string
	Created   time.Time
	Encrypted bool
}

// fileName builds the name of a new backup.
func fileName(kind string, created time.Time, encrypted bool) string {
	k := "full"
	if kind == Incremental {
		k = "incr"
	}
	name := namePrefix + created.UTC().Format(timeLayout) + "-" + k + ".tar.gz"
	if encrypted {
		name += encryptedExt
	}
	return name
}

// parseName reads a backup file name; ok is false for other files.
func parseName(name string) (f File, ok bool) {
	m := namePattern.FindStringSubmatch(name)
	if m == nil {
		return f, false
	}
	t, err := time.ParseInLocation(timeLayout, m[1], time.UTC)
	if err != nil {
		return f, false
	}
	f = File{Name: name, Created: t, Kind: Full, Encrypted: m[3] != ""}
	if m[2] == "incr" {
		f.Kind = Incremental
	}
	return f, true
}

// backupFiles keeps the backups among names and sorts them oldest first.
func backupFiles(all []File) []File {
	var out []File
	for _, f := range all {
		if b, ok := parseName(f.Name); ok {
			b.Size = f.Size
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// hashBlocks returns the SHA-256 of the whole file and of every block.
func hashBlocks(path string) (sum string, size int64, blocks []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, nil, err
	}
	defer f.Close()
	whole := sha256.New()
	buf := make([]byte, BlockSize)
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			whole.Write(buf[:n])
			b := sha256.Sum256(buf[:n])
			blocks = append(blocks, hex.EncodeToString(b[:]))
			size += int64(n)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return "", 0, nil, err
		}
	}
	return hex.EncodeToString(whole.Sum(nil)), size, blocks, nil
}

// changedBlocks lists the blocks of now that differ from before.
func changedBlocks(before, now []string) []int {
	var out []int
	for i, h := range now {
		if i >= len(before) || before[i] != h {
			out = append(out, i)
		}
	}
	return out
}

// writeArchive writes the backup of the database file at dbPath to w,
// encrypted with passphrase when it is set.
func writeArchive(w io.Writer, m Manifest, dbPath, passphrase string) error {
	out := w
	var enc io.WriteCloser
	if passphrase != "" {
		r, err := age.NewScryptRecipient(passphrase)
		if err != nil {
			return err
		}
		if enc, err = age.Encrypt(w, r); err != nil {
			return err
		}
		out = enc
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	mb, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := writeEntry(tw, manifestEntry, int64(len(mb)), m.Created, bytes.NewReader(mb)); err != nil {
		return err
	}
	db, err := os.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if m.Kind == Full {
		err = writeEntry(tw, databaseEntry, m.Size, m.Created, db)
	} else {
		var size int64
		for _, i := range m.Changed {
			size += blockLen(m, i)
		}
		pr, pw := io.Pipe()
		go func() {
			var err error
			for _, i := range m.Changed {
				if _, err = io.Copy(pw, io.NewSectionReader(db, int64(i)*int64(m.BlockSize), blockLen(m, i))); err != nil {
					break
				}
			}
			pw.CloseWithError(err)
		}()
		err = writeEntry(tw, blocksEntry, size, m.Created, pr)
		pr.Close()
	}
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if enc != nil {
		return enc.Close()
	}
	return nil
}

// blockLen is the length of block i; only the last block can be short.
func blockLen(m Manifest, i int) int64 {
	start := int64(i) * int64(m.BlockSize)
	return min(int64(m.BlockSize), m.Size-start)
}

func writeEntry(tw *tar.Writer, name string, size int64, mod time.Time, r io.Reader) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Size: size, Mode: 0o600, ModTime: mod, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	n, err := io.Copy(tw, r)
	if err == nil && n != size {
		err = fmt.Errorf("%s: wrote %d of %d bytes", name, n, size)
	}
	return err
}

// ErrPassphrase is returned when a backup cannot be decrypted.
var ErrPassphrase = errors.New("the backup is encrypted and the passphrase is missing or wrong")

// archiveReader reads a backup archive: first the manifest, then the data.
type archiveReader struct {
	Manifest Manifest
	tr       *tar.Reader
	gz       *gzip.Reader
}

func openArchive(r io.Reader, passphrase string) (*archiveReader, error) {
	br := bufio.NewReader(r)
	if head, _ := br.Peek(len(ageHeader)); string(head) == ageHeader {
		if passphrase == "" {
			return nil, ErrPassphrase
		}
		id, err := age.NewScryptIdentity(passphrase)
		if err != nil {
			return nil, err
		}
		dr, err := age.Decrypt(br, id)
		if err != nil {
			return nil, fmt.Errorf("%w (%v)", ErrPassphrase, err)
		}
		r = dr
	} else {
		r = br
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("not a backup archive: %w", err)
	}
	a := &archiveReader{tr: tar.NewReader(gz), gz: gz}
	h, err := a.tr.Next()
	if err != nil || h.Name != manifestEntry || h.Size > 64<<20 {
		return nil, errors.New("not a backup archive: the manifest is missing")
	}
	if err := json.NewDecoder(a.tr).Decode(&a.Manifest); err != nil {
		return nil, fmt.Errorf("reading the backup manifest: %w", err)
	}
	m := a.Manifest
	if m.Format != formatVersion {
		return nil, fmt.Errorf("backup format %d is not supported; update IPTV Portal", m.Format)
	}
	if m.Kind != Full && m.Kind != Incremental || m.BlockSize <= 0 || m.Size < 0 {
		return nil, errors.New("the backup manifest is invalid")
	}
	return a, nil
}

const ageHeader = "age-encryption.org/"

// apply writes the data of the archive into the database file f: the whole
// file for a full backup, the changed blocks for an incremental one.
func (a *archiveReader) apply(f *os.File) error {
	m := a.Manifest
	h, err := a.tr.Next()
	if err != nil {
		return fmt.Errorf("reading %s: %w", m.Name, err)
	}
	switch {
	case m.Kind == Full && h.Name == databaseEntry:
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := f.Truncate(0); err != nil {
			return err
		}
		if n, err := io.Copy(f, a.tr); err != nil || n != m.Size {
			return fmt.Errorf("reading %s: got %d of %d bytes (%v)", m.Name, n, m.Size, err)
		}
	case m.Kind == Incremental && h.Name == blocksEntry:
		if err := f.Truncate(m.Size); err != nil {
			return err
		}
		for _, i := range m.Changed {
			if i < 0 || i >= len(m.Blocks) {
				return fmt.Errorf("%s: block %d out of range", m.Name, i)
			}
			n := blockLen(m, i)
			if _, err := io.CopyN(io.NewOffsetWriter(f, int64(i)*int64(m.BlockSize)), a.tr, n); err != nil {
				return fmt.Errorf("reading %s: %w", m.Name, err)
			}
		}
	default:
		return fmt.Errorf("%s: unexpected entry %q", m.Name, h.Name)
	}
	return nil
}
