package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// sixStatus reports whether the desktop owner opted in to the SIX kernel
// trial. "ready" means the next boot is staged to try SIX, "downloading:N"
// that N percent of the image has arrived, and "waiting" that the image is
// here but a system update is still on trial, so SIX cannot be staged yet.
func (s *SDBServer) sixStatus() Reply {
	if s.cfg.SixMarker == "" {
		return Reply{Error: "six control not configured"}
	}
	if _, err := os.Stat(s.cfg.SixMarker); err != nil {
		return Reply{OK: true, State: "off"}
	}
	if s.sixStaged() {
		return Reply{OK: true, State: "ready"}
	}
	if pct, ok := s.sixDownload(); ok {
		return Reply{OK: true, State: fmt.Sprintf("downloading:%d", pct)}
	}
	if s.bootTrial() {
		return Reply{OK: true, State: "waiting"}
	}
	return Reply{OK: true, State: "on"}
}

func (s *SDBServer) bootTrial() bool {
	if s.cfg.SixBootState == "" {
		return false
	}
	data, err := os.ReadFile(s.cfg.SixBootState)
	if err != nil {
		return false
	}
	return bytes.Contains(data, []byte("target=next\n")) && bytes.Contains(data, []byte("trial=1\n"))
}

// sixStaged reports a trial the bridge armed itself: the kernel in the next
// slot is the one it recorded, not a system update on its own trial.
func (s *SDBServer) sixStaged() bool {
	if !s.bootTrial() {
		return false
	}
	want, err := os.ReadFile(filepath.Join(filepath.Dir(s.cfg.SixMarker), "staged"))
	if err != nil {
		return false
	}
	f, err := os.Open(filepath.Join(filepath.Dir(s.cfg.SixBootState), "kernelcache-next.efi"))
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return strings.TrimSpace(string(want)) == hex.EncodeToString(h.Sum(nil))
}

func (s *SDBServer) sixDownload() (int, bool) {
	if s.cfg.SixDownload == "" {
		return 0, false
	}
	st, err := os.Stat(s.cfg.SixDownload)
	if err != nil {
		return 0, false
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(s.cfg.SixMarker), "size"))
	if err != nil {
		return 0, false
	}
	total, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || total <= 0 || st.Size() >= total {
		return 0, false
	}
	return int(st.Size() * 100 / total), true
}

// setSixEnabled toggles the opt-in marker read by the bridge unit. Enabling
// clears earlier attempt records so the owner gets one fresh trial; disabling
// cancels a trial that is staged but has not run yet.
func (s *SDBServer) setSixEnabled(on bool) Reply {
	if s.cfg.SixMarker == "" || s.cfg.SixUnit == "" {
		return Reply{Error: "six control not configured"}
	}
	dir := filepath.Dir(s.cfg.SixMarker)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if on {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Reply{Error: err.Error()}
		}
		old, _ := filepath.Glob(filepath.Join(dir, "attempted*"))
		for _, p := range old {
			_ = os.Remove(p)
		}
		if err := writeMarker(s.cfg.SixMarker); err != nil {
			return Reply{Error: err.Error()}
		}
		if err := s.m.StartUnit(ctx, s.cfg.SixUnit); err != nil {
			return Reply{Error: err.Error()}
		}
		return s.sixStatus()
	}
	if err := os.Remove(s.cfg.SixMarker); err != nil && !os.IsNotExist(err) {
		return Reply{Error: err.Error()}
	}
	_ = syncMarkerDir(s.cfg.SixMarker)
	if _, ok := s.m.Get(s.cfg.SixUnit); ok {
		_ = s.m.StopUnit(ctx, s.cfg.SixUnit)
	}
	if s.sixStaged() {
		if err := writeSynced(s.cfg.SixBootState, []byte("target=active\n")); err != nil {
			return Reply{Error: err.Error()}
		}
	}
	return s.sixStatus()
}

func writeMarker(path string) error {
	return writeSynced(path, nil)
}

func writeSynced(path string, data []byte) error {
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncMarkerDir(path)
}
