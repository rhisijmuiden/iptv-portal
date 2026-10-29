package sysinfo

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/activity"
)

// fakeRoot builds a /proc (and optionally /sys/fs/cgroup) under a temp dir.
func fakeRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func write(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const meminfo = "MemTotal:        8000000 kB\nMemFree:  1000000 kB\nMemAvailable:    6000000 kB\nSwapTotal: 1000 kB\nSwapFree: 250 kB\n"

const netdev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 9999 1 0 0 0 0 0 0 9999 1 0 0 0 0 0 0
  eth0: %d 10 0 0 0 0 0 0 %d 10 0 0 0 0 0 0
`

func netDev(rx, tx int) string { return fmt.Sprintf(netdev, rx, tx) }

func newTest(root string, now *time.Time) *Collector {
	c := New("", activity.New())
	c.root = root
	c.now = func() time.Time { return *now }
	return c
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestHost(t *testing.T) {
	root := fakeRoot(t, map[string]string{
		"proc/stat":                 "cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 1 1 1\n",
		"proc/meminfo":              meminfo,
		"proc/loadavg":              "0.50 0.40 0.30 1/100 1234\n",
		"proc/uptime":               "90061.5 1000.0\n",
		"proc/net/dev":              netDev(1000, 500),
		"proc/self/stat":            "42 (iptv portal) S 1 2 3 4 5 6 7 8 9 10 20 30 0 0",
		"proc/self/status":          "Name:\tiptv-portal\nVmRSS:\t   2048 kB\nThreads:\t9\n",
		"etc/os-release":            "NAME=Debian\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n",
		"proc/sys/kernel/osrelease": "6.8.12-pve\n",
	})
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	c := newTest(root, &now)

	s := c.Sample()
	if s.CPU != -1 || s.Process != -1 {
		t.Errorf("first sample CPU = %v/%v, want unknown", s.CPU, s.Process)
	}
	if s.MemTotal != 8000000*1024 || s.MemUsed != 2000000*1024 || s.Load1 != 0.5 {
		t.Errorf("memory/load = %d/%d/%v", s.MemUsed, s.MemTotal, s.Load1)
	}

	// 10 s later: 300 of 1000 ticks busy, 5 MB/s down, the process used 0.5 s.
	now = now.Add(10 * time.Second)
	write(t, root, "proc/stat", "cpu  250 0 250 1300 200 0 0 0 0 0\n")
	write(t, root, "proc/net/dev", netDev(1000+50_000_000, 500+1_000_000))
	write(t, root, "proc/self/stat", "42 (iptv portal) S 1 2 3 4 5 6 7 8 9 10 50 50 0 0")
	s = c.Sample()
	if !near(s.CPU, 30) {
		t.Errorf("CPU = %v, want 30", s.CPU)
	}
	if cpus := c.cpus(); !near(s.Process, 5.0/cpus) {
		t.Errorf("process CPU = %v, want %v", s.Process, 5.0/cpus)
	}
	if !near(s.RxRate, 5_000_000) || !near(s.TxRate, 100_000) {
		t.Errorf("network = %v/%v", s.RxRate, s.TxRate)
	}

	st := c.Status()
	if st.System.OS != "Debian GNU/Linux 13 (trixie)" || st.System.Kernel != "6.8.12-pve" || st.System.Container != "" {
		t.Errorf("system = %+v", st.System)
	}
	if st.System.Uptime != 90061500*time.Millisecond || st.System.Load5 != 0.4 {
		t.Errorf("uptime/load = %v/%v", st.System.Uptime, st.System.Load5)
	}
	if st.System.SwapUsed != 750*1024 || st.System.MemLimited {
		t.Errorf("swap = %d, limited %v", st.System.SwapUsed, st.System.MemLimited)
	}
	if st.Process.RSS != 2048*1024 || st.Process.Threads != 9 || st.Process.Goroutines == 0 {
		t.Errorf("process = %+v", st.Process)
	}
	if len(st.Network.Interfaces) != 1 || st.Network.Interfaces[0].Name != "eth0" {
		t.Errorf("interfaces = %+v", st.Network.Interfaces)
	}
}

func TestContainer(t *testing.T) {
	root := fakeRoot(t, map[string]string{
		"proc/stat":                    "cpu  1 0 1 1 0 0 0 0 0 0\n",
		"proc/meminfo":                 meminfo,
		"run/systemd/container":        "lxc\n",
		"sys/fs/cgroup/cpu.max":        "200000 100000\n",
		"sys/fs/cgroup/cpu.stat":       "usage_usec 1000000\nuser_usec 1\n",
		"sys/fs/cgroup/memory.max":     "1073741824\n",
		"sys/fs/cgroup/memory.current": "600000000\n",
		"sys/fs/cgroup/memory.stat":    "anon 1\nfile 2\ninactive_file 100000000\nactive_file 5\n",
	})
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	c := newTest(root, &now)
	c.Sample()
	now = now.Add(10 * time.Second)
	// 5 CPU seconds in 10 s on a 2-core limit is 25%.
	write(t, root, "sys/fs/cgroup/cpu.stat", "usage_usec 6000000\n")
	s := c.Sample()
	if c.cpus() > 2 || (c.cpus() == 2 && !near(s.CPU, 25)) {
		t.Errorf("CPU = %v on %v cores", s.CPU, c.cpus())
	}
	if s.MemTotal != 1073741824 || s.MemUsed != 500000000 {
		t.Errorf("memory = %d/%d", s.MemUsed, s.MemTotal)
	}
	st := c.Status()
	if st.System.Container != "lxc" || !st.System.MemLimited {
		t.Errorf("container = %q, limited %v", st.System.Container, st.System.MemLimited)
	}
}

func TestHistory(t *testing.T) {
	root := fakeRoot(t, map[string]string{"proc/stat": "cpu  0 0 0 0 0 0 0 0 0 0\n"})
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	c := newTest(root, &now)
	act := c.act
	act.Proxy.Start()
	// Two hours of samples.
	for i := 0; i < 2*3600/5; i++ {
		c.Sample()
		now = now.Add(Interval)
	}
	recent := c.History(false)
	if n := len(recent); n < 719 || n > 721 {
		t.Errorf("%d recent samples, want an hour's worth", n)
	}
	minutes := c.History(true)
	if n := len(minutes); n != 120 {
		t.Errorf("%d minutes, want 120", n)
	}
	if minutes[1].Proxied != 1 || !minutes[1].Time.Equal(minutes[0].Time.Add(time.Minute)) {
		t.Errorf("minute = %+v", minutes[1])
	}
	// More than a day later the oldest minutes are gone.
	now = now.Add(25 * time.Hour)
	c.Sample()
	now = now.Add(time.Minute)
	c.Sample()
	if n := len(c.History(true)); n != 2 {
		t.Errorf("%d minutes after a day, want 2", n)
	}
}

func TestDirSizes(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "portal.db"), make([]byte, 1000), 0o644)
	os.WriteFile(filepath.Join(dir, "portal.db-wal"), make([]byte, 200), 0o644)
	os.Mkdir(filepath.Join(dir, "backups"), 0o755)
	os.WriteFile(filepath.Join(dir, "backups", "portal.db.bak"), make([]byte, 50), 0o644)
	c := New(dir, nil)
	st := c.Status()
	if st.Disk.DB != 1200 || st.Disk.DataDir != 1250 {
		t.Errorf("db %d, dir %d", st.Disk.DB, st.Disk.DataDir)
	}
	if st.Disk.Total == 0 || st.Disk.Free > st.Disk.Total {
		t.Errorf("disk = %+v", st.Disk)
	}
}
