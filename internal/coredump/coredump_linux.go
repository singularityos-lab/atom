// Package coredump stores the core dumps the kernel pipes to it. The kernel
// runs it once per crash with the dump on stdin (kernel.core_pattern starting
// with a pipe) and keeps the crashed process in /proc until it returns, so the
// executable, command line and owner can be read there. Dumps are compressed,
// capped in size, and pruned so crashes never fill the data partition; a
// program that keeps crashing only gets its metadata recorded.
package coredump

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Config bounds what is kept.
type Config struct {
	Dir       string
	Proc      string
	Kmsg      string
	MaxCore   int64
	MaxTotal  int64
	MaxCount  int
	Burst     int
	BurstSpan time.Duration
	MinFree   float64
}

// Default is the system configuration.
func Default() Config {
	return Config{
		Dir:       "/var/lib/sinty/coredump",
		Proc:      "/proc",
		Kmsg:      "/dev/kmsg",
		MaxCore:   1 << 30,
		MaxTotal:  2 << 30,
		MaxCount:  20,
		Burst:     3,
		BurstSpan: 10 * time.Minute,
		MinFree:   0.10,
	}
}

// Record describes one crash.
type Record struct {
	PID       int       `json:"pid"`
	UID       int       `json:"uid"`
	Signal    int       `json:"signal"`
	Time      time.Time `json:"time"`
	Comm      string    `json:"comm"`
	Exe       string    `json:"exe"`
	Cmdline   []string  `json:"cmdline"`
	Core      string    `json:"core,omitempty"`
	CoreBytes int64     `json:"core_bytes"`
	Truncated bool      `json:"truncated,omitempty"`
	Skipped   string    `json:"skipped,omitempty"`
}

// Run handles one crash. args are the core_pattern arguments: pid, signal,
// time and uid.
func Run(cfg Config, args []string, core io.Reader) error {
	lowerPriority()
	if len(args) < 4 {
		return errors.New("coredump: want pid, signal, time and uid")
	}
	pid, err1 := strconv.Atoi(args[0])
	sig, err2 := strconv.Atoi(args[1])
	ts, err3 := strconv.ParseInt(args[2], 10, 64)
	uid, err4 := strconv.Atoi(args[3])
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return fmt.Errorf("coredump: bad arguments: %w", err)
	}
	rec := Record{PID: pid, UID: uid, Signal: sig, Time: time.Unix(ts, 0).UTC()}
	describe(cfg.Proc, &rec)

	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return err
	}
	base := fmt.Sprintf("%s-%s-%d", rec.Time.Format("20060102T150405Z"), safeName(rec.Comm), pid)

	switch {
	case repeated(cfg, rec):
		rec.Skipped = "crash loop"
	case lowOnSpace(cfg):
		rec.Skipped = "low disk space"
	default:
		n, truncated, err := save(filepath.Join(cfg.Dir, base+".core.gz"), core, cfg.MaxCore)
		if err != nil {
			rec.Skipped = err.Error()
		} else {
			rec.Core = base + ".core.gz"
			rec.CoreBytes = n
			rec.Truncated = truncated
		}
	}
	_, _ = io.Copy(io.Discard, core)

	if err := writeRecord(filepath.Join(cfg.Dir, base+".json"), rec); err != nil {
		return err
	}
	prune(cfg)
	announce(cfg.Kmsg, rec)
	return nil
}

func lowerPriority() {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 19)
	const ioprioWhoProcess, ioprioClassIdle = 1, 3
	_, _, _ = syscall.Syscall(syscall.SYS_IOPRIO_SET, ioprioWhoProcess, 0, ioprioClassIdle<<13)
}

func describe(proc string, rec *Record) {
	dir := filepath.Join(proc, strconv.Itoa(rec.PID))
	if b, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
		rec.Comm = strings.TrimSpace(string(b))
	}
	if exe, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
		rec.Exe = exe
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		for _, a := range strings.Split(strings.TrimRight(string(b), "\x00"), "\x00") {
			if a != "" {
				rec.Cmdline = append(rec.Cmdline, a)
			}
		}
	}
	if rec.Comm == "" {
		rec.Comm = "unknown"
	}
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func save(path string, core io.Reader, max int64) (int64, bool, error) {
	f, err := os.OpenFile(path+".part", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, false, err
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	n, err := io.Copy(zw, io.LimitReader(core, max))
	truncated := false
	if err == nil && n == max {
		var one [1]byte
		if k, _ := core.Read(one[:]); k > 0 {
			truncated = true
		}
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path + ".part")
		return 0, false, err
	}
	return n, truncated, os.Rename(path+".part", path)
}

func writeRecord(path string, rec Record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type entry struct {
	base  string
	rec   Record
	bytes int64
}

func entries(dir string) []entry {
	names, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []entry
	for _, n := range names {
		data, err := os.ReadFile(n)
		if err != nil {
			continue
		}
		var e entry
		if json.Unmarshal(data, &e.rec) != nil {
			continue
		}
		e.base = strings.TrimSuffix(filepath.Base(n), ".json")
		if fi, err := os.Stat(filepath.Join(dir, e.base+".core.gz")); err == nil {
			e.bytes = fi.Size()
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rec.Time.Before(out[j].rec.Time) })
	return out
}

func repeated(cfg Config, rec Record) bool {
	n := 0
	for _, e := range entries(cfg.Dir) {
		if e.rec.Exe == rec.Exe && e.rec.Core != "" && rec.Time.Sub(e.rec.Time) < cfg.BurstSpan {
			n++
		}
	}
	return n >= cfg.Burst
}

func lowOnSpace(cfg Config) bool {
	var st syscall.Statfs_t
	if syscall.Statfs(cfg.Dir, &st) != nil || st.Blocks == 0 {
		return false
	}
	return float64(st.Bavail)/float64(st.Blocks) < cfg.MinFree
}

// prune drops the oldest crashes until both the count and the size of the
// stored dumps are within bounds.
func prune(cfg Config) {
	all := entries(cfg.Dir)
	var total int64
	for _, e := range all {
		total += e.bytes
	}
	for len(all) > 0 && (len(all) > cfg.MaxCount || total > cfg.MaxTotal) {
		e := all[0]
		os.Remove(filepath.Join(cfg.Dir, e.base+".core.gz"))
		os.Remove(filepath.Join(cfg.Dir, e.base+".json"))
		total -= e.bytes
		all = all[1:]
	}
	parts, _ := filepath.Glob(filepath.Join(cfg.Dir, "*.part"))
	for _, p := range parts {
		if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) > time.Hour {
			os.Remove(p)
		}
	}
}

func announce(kmsg string, rec Record) {
	f, err := os.OpenFile(kmsg, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	what := rec.Core
	if what == "" {
		what = "no dump: " + rec.Skipped
	}
	fmt.Fprintf(w, "<3>sinit coredump: %s[%d] uid %d killed by signal %d, %s\n", rec.Comm, rec.PID, rec.UID, rec.Signal, what)
	w.Flush()
}
