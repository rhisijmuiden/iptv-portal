package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/version"
)

// stateFile, in the data directory, remembers the previous backup so the
// next one can be incremental, and the outcome of the last run.
const stateFile = "backup-state.json"

// maxChain is the most incremental backups made after a full one, whatever
// FullEvery says, to keep restores quick.
const maxChain = 200

type state struct {
	// Location and Passphrase (a hash of it) the chain was written with;
	// changing either starts a new chain with a full backup.
	Location   string    `json:"location"`
	Passphrase string    `json:"passphrase"`
	Last       string    `json:"last"`
	LastFull   time.Time `json:"last_full"`
	Chain      int       `json:"chain"`
	BlockSize  int       `json:"block_size"`
	Blocks     []string  `json:"blocks"`

	Status Status `json:"status"`
}

// Status is the outcome of the most recent backup.
type Status struct {
	LastAttempt time.Time `json:"last_attempt"`
	LastSuccess time.Time `json:"last_success"`
	// File, Kind and Size describe the last successful backup.
	File  string `json:"file"`
	Kind  string `json:"kind"`
	Size  int64  `json:"size"`
	Error string `json:"error"`
	// Running is set while a backup or restore is in progress.
	Running string `json:"-"`
}

// Service makes backups, on a schedule or on request, and restores them.
type Service struct {
	store *store.Store
	dir   string
	log   *slog.Logger
	now   func() time.Time

	run     sync.Mutex // held while a backup or restore runs
	mu      sync.Mutex // guards running
	running string
}

// New returns the backup service for the database in st.
func New(st *store.Store, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{store: st, dir: st.Dir(), log: log, now: time.Now}
}

func (s *Service) loadState() state {
	var st state
	if b, err := os.ReadFile(filepath.Join(s.dir, stateFile)); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

func (s *Service) saveState(st state) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, stateFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, stateFile))
}

// Status returns the outcome of the last backup and what is running now.
func (s *Service) Status() Status {
	st := s.loadState().Status
	s.mu.Lock()
	st.Running = s.running
	s.mu.Unlock()
	return st
}

// Next is when the next automatic backup is due; zero when they are off.
func (s *Service) Next(c Config) time.Time {
	return c.Next(s.loadState().Status.LastAttempt)
}

func (s *Service) setRunning(what string) {
	s.mu.Lock()
	s.running = what
	s.mu.Unlock()
}

// ErrBusy is returned when a backup or restore is already running.
var ErrBusy = errors.New("a backup or restore is already running")

// Start runs a backup in the background. forceFull makes it a full
// backup; otherwise it is incremental when possible.
func (s *Service) Start(forceFull bool) error {
	if !s.run.TryLock() {
		return ErrBusy
	}
	go func() {
		defer s.run.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		s.backup(ctx, forceFull)
	}()
	return nil
}

// Run makes a backup and waits for it.
func (s *Service) Run(ctx context.Context, forceFull bool) (File, error) {
	if !s.run.TryLock() {
		return File{}, ErrBusy
	}
	defer s.run.Unlock()
	return s.backup(ctx, forceFull)
}

// Loop makes the scheduled backups until ctx ends.
func (s *Service) Loop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, err := LoadConfig(ctx, s.store)
		if err != nil {
			s.log.Warn("reading backup settings", "err", err)
			continue
		}
		next := s.Next(c)
		if c.Schedule == Off || !c.Location.Configured() || s.now().Before(next) {
			continue
		}
		if !s.run.TryLock() {
			continue
		}
		s.backup(ctx, false)
		s.run.Unlock()
	}
}

// backup makes one backup; the caller holds s.run.
func (s *Service) backup(ctx context.Context, forceFull bool) (File, error) {
	s.setRunning("backup")
	defer s.setRunning("")
	st := s.loadState()
	st.Status.LastAttempt = s.now()
	f, err := s.makeBackup(ctx, &st, forceFull)
	if err != nil {
		st.Status.Error = err.Error()
		s.log.Warn("backup failed", "err", err)
	} else {
		st.Status.Error = ""
		st.Status.LastSuccess = s.now()
		st.Status.File, st.Status.Kind, st.Status.Size = f.Name, f.Kind, f.Size
		s.log.Info("backup made", "file", f.Name, "kind", f.Kind, "bytes", f.Size)
	}
	if serr := s.saveState(st); serr != nil {
		s.log.Warn("saving backup state", "err", serr)
	}
	return f, err
}

func passphraseID(p string) string {
	if p == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("iptv-portal-backup:" + p))
	return hex.EncodeToString(sum[:8])
}

func (s *Service) makeBackup(ctx context.Context, st *state, forceFull bool) (File, error) {
	c, err := LoadConfig(ctx, s.store)
	if err != nil {
		return File{}, err
	}
	loc := c.Location
	if !loc.Configured() {
		return File{}, errors.New("no remote location is set up")
	}
	t, err := dialer(ctx, loc)
	if err != nil {
		return File{}, err
	}
	defer t.Close()
	remote, err := t.List(ctx)
	if err != nil {
		return File{}, err
	}
	files := backupFiles(remote)

	snap, err := os.CreateTemp(s.dir, ".backup-snapshot-*")
	if err != nil {
		return File{}, err
	}
	defer os.Remove(snap.Name())
	err = s.store.Snapshot(ctx, snap)
	if cerr := snap.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return File{}, fmt.Errorf("copying the database: %w", err)
	}
	sum, size, blocks, err := hashBlocks(snap.Name())
	if err != nil {
		return File{}, err
	}

	now := s.now()
	kind := Incremental
	switch {
	case forceFull, st.Last == "", st.Location != loc.String(), st.Passphrase != passphraseID(c.Passphrase),
		st.BlockSize != BlockSize, st.Chain >= maxChain,
		now.Sub(st.LastFull) >= time.Duration(c.FullEvery)*24*time.Hour, !hasFile(files, st.Last):
		kind = Full
	}
	m := Manifest{Format: formatVersion, Kind: kind, Created: now.UTC().Truncate(time.Second), Version: version.Version,
		Size: size, SHA256: sum, BlockSize: BlockSize, Blocks: blocks}
	m.Name = fileName(kind, m.Created, c.Passphrase != "")
	for hasFile(files, m.Name) || m.Name == st.Last { // two backups in one second
		m.Created = m.Created.Add(time.Second)
		m.Name = fileName(kind, m.Created, c.Passphrase != "")
	}
	if kind == Incremental {
		m.Parent = st.Last
		m.Changed = changedBlocks(st.Blocks, blocks)
	}

	arch, err := os.CreateTemp(s.dir, ".backup-archive-*")
	if err != nil {
		return File{}, err
	}
	defer os.Remove(arch.Name())
	defer arch.Close()
	if err := writeArchive(arch, m, snap.Name(), c.Passphrase); err != nil {
		return File{}, fmt.Errorf("writing the backup: %w", err)
	}
	asize, err := arch.Seek(0, 1)
	if err != nil {
		return File{}, err
	}
	if _, err := arch.Seek(0, 0); err != nil {
		return File{}, err
	}
	if err := t.Upload(ctx, m.Name, arch, asize); err != nil {
		return File{}, err
	}

	st.Location, st.Passphrase, st.Last = loc.String(), passphraseID(c.Passphrase), m.Name
	st.BlockSize, st.Blocks = BlockSize, blocks
	if kind == Full {
		st.LastFull, st.Chain = now, 0
	} else {
		st.Chain++
	}
	f := File{Name: m.Name, Size: asize, Kind: kind, Created: m.Created, Encrypted: c.Passphrase != ""}
	files = append(files, f)
	if err := s.prune(ctx, t, files, c.Keep); err != nil {
		s.log.Warn("deleting old backups", "err", err)
	}
	return f, nil
}

func hasFile(files []File, name string) bool {
	for _, f := range files {
		if f.Name == name {
			return true
		}
	}
	return false
}

// prune deletes everything older than the keep-th newest full backup.
func (s *Service) prune(ctx context.Context, t Target, files []File, keep int) error {
	var fulls []File
	for _, f := range files {
		if f.Kind == Full {
			fulls = append(fulls, f)
		}
	}
	if keep < 1 || len(fulls) <= keep {
		return nil
	}
	sort.Slice(fulls, func(i, j int) bool { return fulls[i].Name > fulls[j].Name })
	cutoff := fulls[keep-1].Created
	for _, f := range files {
		if f.Created.Before(cutoff) {
			if err := t.Delete(ctx, f.Name); err != nil {
				return err
			}
			s.log.Info("deleted old backup", "file", f.Name)
		}
	}
	return nil
}

// Remote lists the backups on the remote location, newest first.
func (s *Service) Remote(ctx context.Context) ([]File, error) {
	c, err := LoadConfig(ctx, s.store)
	if err != nil {
		return nil, err
	}
	if !c.Location.Configured() {
		return nil, errors.New("no remote location is set up")
	}
	t, err := dialer(ctx, c.Location)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	all, err := t.List(ctx)
	if err != nil {
		return nil, err
	}
	files := backupFiles(all)
	sort.Slice(files, func(i, j int) bool { return files[i].Name > files[j].Name })
	return files, nil
}

// Restore downloads the backup name, and the backups it builds on, and
// stages the database they restore to. It takes effect when the portal
// restarts (see store.PendingRestore). It returns the manifest of name.
func (s *Service) Restore(ctx context.Context, name string) (Manifest, error) {
	if !s.run.TryLock() {
		return Manifest{}, ErrBusy
	}
	defer s.run.Unlock()
	s.setRunning("restore")
	defer s.setRunning("")
	m, err := s.restore(ctx, name)
	if err != nil {
		s.log.Warn("restore failed", "file", name, "err", err)
		return m, err
	}
	s.log.Info("restore staged, restart to apply", "file", name, "made", m.Created, "version", m.Version)
	return m, nil
}

func (s *Service) restore(ctx context.Context, name string) (Manifest, error) {
	if _, ok := parseName(name); !ok {
		return Manifest{}, fmt.Errorf("%q is not a backup file", name)
	}
	c, err := LoadConfig(ctx, s.store)
	if err != nil {
		return Manifest{}, err
	}
	if !c.Location.Configured() {
		return Manifest{}, errors.New("no remote location is set up")
	}
	t, err := dialer(ctx, c.Location)
	if err != nil {
		return Manifest{}, err
	}
	defer t.Close()

	tmp, err := os.MkdirTemp(s.dir, ".restore-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(tmp)

	// Download the chain from the chosen backup back to its full backup.
	var chain []string // archive paths, newest first
	var target Manifest
	for next := name; ; {
		if len(chain) > maxChain*2 {
			return target, errors.New("the chain of incremental backups is too long")
		}
		if _, ok := parseName(next); !ok {
			return target, fmt.Errorf("the backup refers to %q, which is not a backup file", next)
		}
		p := filepath.Join(tmp, next)
		if err := download(ctx, t, next, p); err != nil {
			return target, err
		}
		m, err := readManifest(p, c.Passphrase)
		if err != nil {
			return target, fmt.Errorf("%s: %w", next, err)
		}
		if len(chain) == 0 {
			target = m
		}
		chain = append(chain, p)
		if m.Kind == Full {
			break
		}
		if m.Parent == "" {
			return target, fmt.Errorf("%s: the incremental backup names no parent", next)
		}
		next = m.Parent
	}

	db := filepath.Join(tmp, "portal.db")
	f, err := os.OpenFile(db, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return target, err
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if err := applyArchive(chain[i], c.Passphrase, f); err != nil {
			f.Close()
			return target, err
		}
	}
	if err := f.Close(); err != nil {
		return target, err
	}
	sum, size, _, err := hashBlocks(db)
	if err != nil {
		return target, err
	}
	if sum != target.SHA256 || size != target.Size {
		return target, errors.New("the restored database does not match the backup; a backup in the chain may be damaged")
	}
	if _, err := store.CheckDatabaseFile(ctx, db); err != nil {
		return target, err
	}
	if err := os.Rename(db, filepath.Join(s.dir, store.PendingRestore)); err != nil {
		return target, err
	}
	// The restored database differs from what the last backup recorded, so
	// start the next chain with a full backup.
	st := s.loadState()
	st.Last, st.Blocks = "", nil
	s.saveState(st)
	return target, nil
}

func download(ctx context.Context, t Target, name, dst string) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = t.Download(ctx, name, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func readManifest(path, passphrase string) (Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	a, err := openArchive(f, passphrase)
	if err != nil {
		return Manifest{}, err
	}
	return a.Manifest, nil
}

func applyArchive(path, passphrase string, db *os.File) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	a, err := openArchive(f, passphrase)
	if err != nil {
		return err
	}
	return a.apply(db)
}
