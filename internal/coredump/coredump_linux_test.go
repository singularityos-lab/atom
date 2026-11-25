package coredump

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	cfg := Default()
	cfg.Dir = filepath.Join(t.TempDir(), "coredump")
	cfg.Proc = t.TempDir()
	cfg.Kmsg = filepath.Join(t.TempDir(), "kmsg")
	cfg.MinFree = 0
	return cfg
}

func fakeProc(t *testing.T, cfg Config, pid int, comm, exe string) {
	dir := filepath.Join(cfg.Proc, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "cmdline"), []byte(exe+"\x00--flag\x00"), 0o644)
	if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
		t.Fatal(err)
	}
}

func records(t *testing.T, dir string) []Record {
	names, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []Record
	for _, n := range names {
		data, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		var r Record
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func crash(t *testing.T, cfg Config, pid int, at time.Time, core []byte) {
	t.Helper()
	args := []string{strconv.Itoa(pid), "11", strconv.FormatInt(at.Unix(), 10), "1000"}
	if err := Run(cfg, args, bytes.NewReader(core)); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestStoresCompressedCoreAndRecord(t *testing.T) {
	cfg := testConfig(t)
	os.WriteFile(cfg.Kmsg, nil, 0o644)
	fakeProc(t, cfg, 42, "labwc", "/usr/bin/labwc")
	core := bytes.Repeat([]byte("ELF core "), 1000)
	crash(t, cfg, 42, time.Now(), core)

	recs := records(t, cfg.Dir)
	if len(recs) != 1 {
		t.Fatalf("%d records", len(recs))
	}
	r := recs[0]
	if r.Comm != "labwc" || r.Exe != "/usr/bin/labwc" || r.Signal != 11 || r.UID != 1000 || len(r.Cmdline) != 2 {
		t.Fatalf("record %+v", r)
	}
	f, err := os.Open(filepath.Join(cfg.Dir, r.Core))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, core) || r.CoreBytes != int64(len(core)) {
		t.Fatal("stored core differs from the dump")
	}
	if log, _ := os.ReadFile(cfg.Kmsg); !strings.Contains(string(log), "labwc[42]") {
		t.Fatalf("kmsg line missing: %q", log)
	}
}

func TestTruncatesOversizedCores(t *testing.T) {
	cfg := testConfig(t)
	cfg.MaxCore = 100
	fakeProc(t, cfg, 7, "big", "/usr/bin/big")
	crash(t, cfg, 7, time.Now(), make([]byte, 1000))
	r := records(t, cfg.Dir)[0]
	if !r.Truncated || r.CoreBytes != 100 {
		t.Fatalf("record %+v", r)
	}
}

func TestCrashLoopKeepsOnlyMetadata(t *testing.T) {
	cfg := testConfig(t)
	fakeProc(t, cfg, 9, "loop", "/usr/bin/loop")
	now := time.Now()
	for i := 0; i < cfg.Burst+2; i++ {
		crash(t, cfg, 9, now.Add(time.Duration(i)*time.Second), []byte("core"))
	}
	cores, _ := filepath.Glob(filepath.Join(cfg.Dir, "*.core.gz"))
	if len(cores) != cfg.Burst {
		t.Fatalf("%d cores kept, want %d", len(cores), cfg.Burst)
	}
	if n := len(records(t, cfg.Dir)); n != cfg.Burst+2 {
		t.Fatalf("%d records, want %d", n, cfg.Burst+2)
	}
}

func TestPrunesOldestBeyondCount(t *testing.T) {
	cfg := testConfig(t)
	cfg.MaxCount = 3
	now := time.Now()
	for i := 0; i < 6; i++ {
		pid := 100 + i
		fakeProc(t, cfg, pid, "p"+strconv.Itoa(i), "/usr/bin/p"+strconv.Itoa(i))
		crash(t, cfg, pid, now.Add(time.Duration(i)*time.Second), []byte("core"))
	}
	recs := records(t, cfg.Dir)
	if len(recs) != 3 {
		t.Fatalf("%d records left", len(recs))
	}
	for _, r := range recs {
		if r.PID < 103 {
			t.Fatalf("kept an old crash: %+v", r)
		}
	}
}

func TestBadArguments(t *testing.T) {
	cfg := testConfig(t)
	if err := Run(cfg, []string{"1", "2"}, bytes.NewReader(nil)); err == nil {
		t.Fatal("accepted missing arguments")
	}
}
