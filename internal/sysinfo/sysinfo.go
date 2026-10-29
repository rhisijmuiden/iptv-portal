// Package sysinfo measures how heavily the server is loaded, for the
// system resources page of the admin panel: CPU, memory, disk, network and
// the portal process itself.
//
// Everything is read from /proc and /sys, so it works on a plain Linux
// host, in a VM and in a Proxmox LXC container. In a container the cgroup
// limits (cores, memory) are used as the totals, because that is what the
// portal can actually use; /proc in a container often still shows the
// host's numbers.
//
// A Collector takes a sample every few seconds and keeps the last hour in
// full and the last day per minute. History is kept in memory and starts
// empty after a restart.
package sysinfo

import (
	"bufio"
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/activity"
)

// Interval is how often the collector takes a sample.
const Interval = 5 * time.Second

const (
	keepRecent  = time.Hour      // samples kept at Interval
	keepMinutes = 24 * time.Hour // per-minute averages kept
	dirEvery    = time.Minute    // how often the data directory is measured
)

// Sample is the load at one moment. Percentages are 0-100; rates are per
// second. A negative CPU means it is not known yet (the first sample).
type Sample struct {
	Time     time.Time `json:"t"`
	CPU      float64   `json:"cpu"`
	Process  float64   `json:"proc"` // CPU used by the portal itself
	MemUsed  uint64    `json:"mem"`
	MemTotal uint64    `json:"memTotal"`
	Load1    float64   `json:"load"`
	RxRate   float64   `json:"rx"`
	TxRate   float64   `json:"tx"`
	Online   int       `json:"online"`
	Watching int       `json:"watching"`
	Proxied  int       `json:"proxied"`  // On Demand streams through the portal
	ProxyBps float64   `json:"proxyBps"` // their traffic
}

// MemPercent is the memory in use as a percentage of the total.
func (s Sample) MemPercent() float64 {
	if s.MemTotal == 0 {
		return 0
	}
	return float64(s.MemUsed) * 100 / float64(s.MemTotal)
}

// Status is everything the system resources page shows.
type Status struct {
	Now    Sample `json:"now"`
	System struct {
		Hostname  string        `json:"hostname"`
		OS        string        `json:"os"`
		Kernel    string        `json:"kernel"`
		Container string        `json:"container"` // "lxc", ... or "" on a host or VM
		CPUs      float64       `json:"cpus"`      // cores available to the portal
		Uptime    time.Duration `json:"uptime"`
		Load5     float64       `json:"load5"`
		Load15    float64       `json:"load15"`
		SwapUsed  uint64        `json:"swapUsed"`
		SwapTotal uint64        `json:"swapTotal"`
		// MemLimited tells the memory total is a container limit.
		MemLimited bool `json:"memLimited"`
	} `json:"system"`
	Disk struct {
		Path    string `json:"path"`
		Total   uint64 `json:"total"`
		Used    uint64 `json:"used"`
		Free    uint64 `json:"free"`
		DataDir int64  `json:"dataDir"` // size of the data directory
		DB      int64  `json:"db"`      // size of the database (with its WAL)
	} `json:"disk"`
	Network struct {
		Interfaces []Interface `json:"interfaces"`
	} `json:"network"`
	Process struct {
		PID        int           `json:"pid"`
		Uptime     time.Duration `json:"uptime"`
		RSS        uint64        `json:"rss"`
		Heap       uint64        `json:"heap"`
		GoSys      uint64        `json:"goSys"`
		Goroutines int           `json:"goroutines"`
		Threads    int           `json:"threads"`
		OpenFiles  int           `json:"openFiles"`
		GCRuns     uint32        `json:"gcRuns"`
		GoVersion  string        `json:"goVersion"`
	} `json:"process"`
	ProxyTotal uint64 `json:"proxyTotal"` // bytes proxied since the start
}

// Interface is the traffic of one network interface since boot.
type Interface struct {
	Name string `json:"name"`
	Rx   uint64 `json:"rx"`
	Tx   uint64 `json:"tx"`
}

// Collector samples the load. Create one with New and run Loop.
type Collector struct {
	root    string // "/" except in tests
	dataDir string
	act     *activity.Tracker
	start   time.Time
	now     func() time.Time

	mu      sync.Mutex
	recent  []Sample // at Interval, oldest first
	minutes []Sample // per-minute averages, oldest first
	bucket  []Sample // samples of the minute being collected
	prev    counters
	dirAt   time.Time
	dirSize int64
	dbSize  int64
}

// counters are the running totals rates are worked out from.
type counters struct {
	at       time.Time
	cpuBusy  float64 // seconds of CPU used, all cores together
	cpuTotal float64 // seconds of CPU available; 0 in a cgroup
	procCPU  float64
	rx, tx   uint64
	proxied  uint64
	ok       bool
}

// New returns a collector for the server the portal runs on; dataDir is
// where the database lives. act may be nil.
func New(dataDir string, act *activity.Tracker) *Collector {
	if act == nil {
		act = activity.New()
	}
	return &Collector{root: "/", dataDir: dataDir, act: act, start: time.Now(), now: time.Now}
}

// Loop samples every Interval until ctx is done.
func (c *Collector) Loop(ctx context.Context) {
	c.Sample()
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Sample()
		}
	}
}

func (c *Collector) path(p string) string { return filepath.Join(c.root, p) }

func (c *Collector) read(p string) string {
	b, err := os.ReadFile(c.path(p))
	if err != nil {
		return ""
	}
	return string(b)
}

// Sample takes a sample now, adds it to the history and returns it.
func (c *Collector) Sample() Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	s := Sample{Time: now, CPU: -1, Process: -1}
	cur := counters{at: now, ok: true}

	cpus := c.cpus()
	if usec, ok := c.cgroupCPU(); ok {
		cur.cpuBusy = usec / 1e6
	} else {
		cur.cpuBusy, cur.cpuTotal = c.procStat()
	}
	cur.procCPU = c.processCPU()
	mem := c.memory()
	s.MemUsed, s.MemTotal = mem.used, mem.total
	s.Load1, _, _ = c.loadavg()
	for _, i := range c.interfaces() {
		cur.rx += i.Rx
		cur.tx += i.Tx
	}
	snap := c.act.Snapshot()
	s.Online, s.Watching = snap.OnlineUsers, snap.Watching
	s.Proxied = c.act.Proxy.Streams()
	cur.proxied = c.act.Proxy.Bytes()

	if p := c.prev; p.ok {
		secs := now.Sub(p.at).Seconds()
		if secs > 0 {
			busy := cur.cpuBusy - p.cpuBusy
			switch {
			case cur.cpuTotal > 0: // /proc/stat: busy share of all time
				if d := cur.cpuTotal - p.cpuTotal; d > 0 {
					s.CPU = clamp(busy * 100 / d)
				}
			case cpus > 0: // cgroup: busy share of the cores available
				s.CPU = clamp(busy * 100 / (secs * cpus))
			}
			if cpus > 0 {
				s.Process = clamp((cur.procCPU - p.procCPU) * 100 / (secs * cpus))
			}
			s.RxRate = rate(cur.rx, p.rx, secs)
			s.TxRate = rate(cur.tx, p.tx, secs)
			s.ProxyBps = rate(cur.proxied, p.proxied, secs)
		}
	}
	c.prev = cur
	c.add(s)
	return s
}

func rate(cur, prev uint64, secs float64) float64 {
	if cur < prev { // counter reset, e.g. an interface came back
		return 0
	}
	return float64(cur-prev) / secs
}

func clamp(p float64) float64 {
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	}
	return p
}

// add keeps s in the history. The caller holds c.mu.
func (c *Collector) add(s Sample) {
	c.recent = append(c.recent, s)
	for len(c.recent) > 0 && s.Time.Sub(c.recent[0].Time) > keepRecent {
		c.recent = c.recent[1:]
	}
	if len(c.bucket) > 0 && !s.Time.Truncate(time.Minute).Equal(c.bucket[0].Time.Truncate(time.Minute)) {
		c.minutes = append(c.minutes, average(c.bucket))
		c.bucket = c.bucket[:0]
		for len(c.minutes) > 0 && s.Time.Sub(c.minutes[0].Time) > keepMinutes {
			c.minutes = c.minutes[1:]
		}
	}
	c.bucket = append(c.bucket, s)
}

// average combines the samples of one minute: the mean of the rates and
// percentages, the highest of the counts.
func average(ss []Sample) Sample {
	a := Sample{Time: ss[0].Time.Truncate(time.Minute), CPU: -1, Process: -1}
	var cpu, proc, n, np float64
	var mem uint64
	for _, s := range ss {
		if s.CPU >= 0 {
			cpu += s.CPU
			n++
		}
		if s.Process >= 0 {
			proc += s.Process
			np++
		}
		mem += s.MemUsed / uint64(len(ss))
		a.MemTotal = s.MemTotal
		a.Load1 += s.Load1 / float64(len(ss))
		a.RxRate += s.RxRate / float64(len(ss))
		a.TxRate += s.TxRate / float64(len(ss))
		a.ProxyBps += s.ProxyBps / float64(len(ss))
		a.Online = max(a.Online, s.Online)
		a.Watching = max(a.Watching, s.Watching)
		a.Proxied = max(a.Proxied, s.Proxied)
	}
	if n > 0 {
		a.CPU = cpu / n
	}
	if np > 0 {
		a.Process = proc / np
	}
	a.MemUsed = mem
	return a
}

// History returns the samples of the last hour (minutes false) or the
// per-minute averages of the last day (minutes true), oldest first.
func (c *Collector) History(minutes bool) []Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !minutes {
		return append([]Sample(nil), c.recent...)
	}
	out := append([]Sample(nil), c.minutes...)
	if len(c.bucket) > 0 { // the minute so far
		out = append(out, average(c.bucket))
	}
	return out
}

// Status returns the latest sample with everything else the page shows.
func (c *Collector) Status() Status {
	c.mu.Lock()
	var st Status
	if len(c.recent) > 0 {
		st.Now = c.recent[len(c.recent)-1]
	} else {
		st.Now = Sample{Time: c.now(), CPU: -1, Process: -1}
	}
	c.mu.Unlock()

	sys := &st.System
	sys.Hostname, _ = os.Hostname()
	sys.OS = c.osName()
	sys.Kernel = strings.TrimSpace(c.read("proc/sys/kernel/osrelease"))
	sys.Container = c.container()
	sys.CPUs = c.cpus()
	if f := strings.Fields(c.read("proc/uptime")); len(f) > 0 {
		if v, err := strconv.ParseFloat(f[0], 64); err == nil {
			sys.Uptime = time.Duration(v * float64(time.Second))
		}
	}
	_, sys.Load5, sys.Load15 = c.loadavg()
	mem := c.memory()
	sys.SwapUsed, sys.SwapTotal, sys.MemLimited = mem.swapUsed, mem.swapTotal, mem.limited

	st.Disk.Path = c.dataDir
	st.Disk.Total, st.Disk.Used, st.Disk.Free = diskSpace(c.dataDir)
	st.Disk.DataDir, st.Disk.DB = c.dirSizes()

	st.Network.Interfaces = c.interfaces()

	p := &st.Process
	p.PID = os.Getpid()
	p.Uptime = c.now().Sub(c.start)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	p.Heap, p.GoSys, p.GCRuns = ms.HeapAlloc, ms.Sys, ms.NumGC
	p.Goroutines = runtime.NumGoroutine()
	p.GoVersion = runtime.Version()
	status := c.read("proc/self/status")
	p.RSS = kB(field(status, "VmRSS:"))
	p.Threads, _ = strconv.Atoi(field(status, "Threads:"))
	if fds, err := os.ReadDir(c.path("proc/self/fd")); err == nil {
		p.OpenFiles = len(fds)
	}
	st.ProxyTotal = c.act.Proxy.Bytes()
	return st
}

// dirSizes returns the size of the data directory and of the database,
// measured at most once a minute.
func (c *Collector) dirSizes() (dir, db int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dataDir == "" {
		return 0, 0
	}
	if now := c.now(); now.Sub(c.dirAt) >= dirEvery {
		c.dirAt = now
		c.dirSize, c.dbSize = 0, 0
		filepath.WalkDir(c.dataDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			c.dirSize += info.Size()
			if filepath.Dir(p) == filepath.Clean(c.dataDir) && strings.HasPrefix(d.Name(), "portal.db") {
				c.dbSize += info.Size()
			}
			return nil
		})
	}
	return c.dirSize, c.dbSize
}

// ---- reading /proc and /sys ----

// cgroupDir is the cgroup v2 directory of the container, or "" when the
// portal does not run in a container with its own cgroup (a plain host or
// VM). Inside a container the cgroup namespace makes /sys/fs/cgroup the
// container's own cgroup, which has a cpu.max; the real root has none.
func (c *Collector) cgroupDir() string {
	d := c.path("sys/fs/cgroup")
	if _, err := os.Stat(filepath.Join(d, "cpu.max")); err != nil {
		return ""
	}
	return d
}

// cgroupCPU returns the CPU microseconds the container used.
func (c *Collector) cgroupCPU() (float64, bool) {
	d := c.cgroupDir()
	if d == "" {
		return 0, false
	}
	b, err := os.ReadFile(filepath.Join(d, "cpu.stat"))
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(field(string(b), "usage_usec"), 64)
	return v, err == nil
}

// procStat returns the busy and total CPU seconds of all cores from
// /proc/stat.
func (c *Collector) procStat() (busy, total float64) {
	line, _, _ := strings.Cut(c.read("proc/stat"), "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0
	}
	for i, v := range f[1:] {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			continue
		}
		if i >= 8 { // guest time is already counted in user time
			break
		}
		total += n
		if i != 3 && i != 4 { // idle, iowait
			busy += n
		}
	}
	return busy / clockTicks, total / clockTicks
}

// clockTicks is USER_HZ, which is 100 on every Linux architecture Go runs on.
const clockTicks = 100

// processCPU returns the CPU seconds used by the portal process.
func (c *Collector) processCPU() float64 {
	s := c.read("proc/self/stat")
	// The command name is in parentheses and may hold spaces.
	if i := strings.LastIndexByte(s, ')'); i >= 0 {
		s = s[i+1:]
	}
	f := strings.Fields(s)
	if len(f) < 13 {
		return 0
	}
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	return (ut + st) / clockTicks
}

// cpus returns how many cores the portal can use: the container's CPU
// limit when there is one, else the cores it may run on.
func (c *Collector) cpus() float64 {
	n := float64(runtime.NumCPU())
	if d := c.cgroupDir(); d != "" {
		f := strings.Fields(c.readAbs(filepath.Join(d, "cpu.max")))
		if len(f) == 2 && f[0] != "max" {
			q, err1 := strconv.ParseFloat(f[0], 64)
			p, err2 := strconv.ParseFloat(f[1], 64)
			if err1 == nil && err2 == nil && p > 0 && q/p < n {
				return q / p
			}
		}
	}
	return n
}

func (c *Collector) readAbs(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

type memInfo struct {
	used, total         uint64
	swapUsed, swapTotal uint64
	limited             bool
}

func (c *Collector) memory() memInfo {
	mi := c.read("proc/meminfo")
	m := memInfo{total: kB(field(mi, "MemTotal:")), swapTotal: kB(field(mi, "SwapTotal:"))}
	avail := kB(field(mi, "MemAvailable:"))
	if avail <= m.total {
		m.used = m.total - avail
	}
	if free := kB(field(mi, "SwapFree:")); free <= m.swapTotal {
		m.swapUsed = m.swapTotal - free
	}
	// In a container with a memory limit, the limit is the total and the
	// cgroup's own use (without page cache that can be dropped) is used.
	if d := c.cgroupDir(); d != "" {
		max, err := strconv.ParseUint(strings.TrimSpace(c.readAbs(filepath.Join(d, "memory.max"))), 10, 64)
		cur, err2 := strconv.ParseUint(strings.TrimSpace(c.readAbs(filepath.Join(d, "memory.current"))), 10, 64)
		if err == nil && err2 == nil && max > 0 && (m.total == 0 || max <= m.total) {
			inactive, _ := strconv.ParseUint(field(c.readAbs(filepath.Join(d, "memory.stat")), "inactive_file"), 10, 64)
			if inactive < cur {
				cur -= inactive
			}
			m.total, m.used, m.limited = max, min(cur, max), true
		}
	}
	return m
}

func (c *Collector) loadavg() (l1, l5, l15 float64) {
	f := strings.Fields(c.read("proc/loadavg"))
	if len(f) < 3 {
		return 0, 0, 0
	}
	l1, _ = strconv.ParseFloat(f[0], 64)
	l5, _ = strconv.ParseFloat(f[1], 64)
	l15, _ = strconv.ParseFloat(f[2], 64)
	return
}

// interfaces lists the network interfaces except loopback.
func (c *Collector) interfaces() []Interface {
	var out []Interface
	sc := bufio.NewScanner(bytes.NewReader([]byte(c.read("proc/net/dev"))))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if name == "lo" || len(f) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		out = append(out, Interface{Name: name, Rx: rx, Tx: tx})
	}
	return out
}

func (c *Collector) osName() string {
	for _, line := range strings.Split(c.read("etc/os-release"), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return runtime.GOOS
}

// container names the container technology the portal runs in, as systemd
// detected it, or "" on a host or VM.
func (c *Collector) container() string {
	if v := strings.TrimSpace(c.read("run/systemd/container")); v != "" {
		return v
	}
	if c.cgroupDir() != "" {
		return "container"
	}
	return ""
}

// field returns the first word after key in a "key value" file.
func field(s, key string) string {
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(line, key); ok && (strings.HasSuffix(key, ":") || strings.HasPrefix(v, " ")) {
			if f := strings.Fields(v); len(f) > 0 {
				return f[0]
			}
		}
	}
	return ""
}

// kB parses a /proc size in kB to bytes.
func kB(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v * 1024
}
