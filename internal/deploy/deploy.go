package deploy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SchemaVersion is the current document version.
const SchemaVersion = 1

// DefaultPath is where the document lives on the writable /var.
const DefaultPath = "/var/lib/atom/deployment.json"

// Health is a slot's last-known health.
type Health string

const (
	Good    Health = "good"
	Bad     Health = "bad"
	Unknown Health = "unknown"
)

// Slot describes one bootable deployment.
type Slot struct {
	Roothash    string `json:"roothash"`
	Version     string `json:"version"`
	Kernel      string `json:"kernel,omitempty"`
	InstalledAt string `json:"installed_at,omitempty"`
	Health      Health `json:"health"`
}

// Boot is the rollback/bootcount state PID 1 maintains.
type Boot struct {
	MaxAttempts       int    `json:"max_attempts"`
	AttemptsRemaining int    `json:"attempts_remaining"`
	Confirmed         bool   `json:"confirmed"`
	LastConfirmedAt   string `json:"last_confirmed_at,omitempty"`
}

// Deployment is the whole document.
type Deployment struct {
	SchemaVersion int             `json:"schema_version"`
	MachineID     string          `json:"machine_id,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
	Current       string          `json:"current"`
	Next          string          `json:"next"`
	Slots         map[string]Slot `json:"slots"`
	Boot          Boot            `json:"boot"`
}

// NewSingleSlot returns a fresh single-slot document (the shape Sinty has today).
func NewSingleSlot(roothash, version string) *Deployment {
	return &Deployment{
		SchemaVersion: SchemaVersion,
		Current:       "a",
		Next:          "a",
		Slots: map[string]Slot{
			"a": {Roothash: roothash, Version: version, Health: Unknown},
		},
		Boot: Boot{MaxAttempts: 3, AttemptsRemaining: 3, Confirmed: false},
	}
}

// Load reads and decodes the document.
func Load(path string) (*Deployment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d Deployment
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("deployment.json: %w", err)
	}
	return &d, nil
}

// Save writes the document atomically: temp file, fsync, rename, fsync dir.
func (d *Deployment) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return fsyncDir(filepath.Dir(path))
}

func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// BeginBoot records a boot attempt of the current slot: it decrements the
// attempts counter and clears the confirmed flag. The boot manager
// (sd-boot bootcount or the initramfs) may do this instead; calling it here is
// idempotent-safe for a single-slot system where rollback is a no-op.
func (d *Deployment) BeginBoot() {
	d.Boot.Confirmed = false
	if d.Boot.AttemptsRemaining > 0 {
		d.Boot.AttemptsRemaining--
	}
}

// Confirm marks the current boot good: it restores the attempts counter, sets
// the confirmed flag and timestamp, and marks the current slot healthy.
func (d *Deployment) Confirm(now string) {
	d.Boot.Confirmed = true
	d.Boot.AttemptsRemaining = d.Boot.MaxAttempts
	d.Boot.LastConfirmedAt = now
	d.UpdatedAt = now
	if s, ok := d.Slots[d.Current]; ok {
		s.Health = Good
		d.Slots[d.Current] = s
	}
}

// ShouldRollback reports whether the boot manager should fall back to the other
// slot: the current boot was never confirmed and the attempts are exhausted.
func (d *Deployment) ShouldRollback() bool {
	return !d.Boot.Confirmed && d.Boot.AttemptsRemaining <= 0
}

// MarkCurrentBad flags the current slot as bad (used before a rollback).
func (d *Deployment) MarkCurrentBad() {
	if s, ok := d.Slots[d.Current]; ok {
		s.Health = Bad
		d.Slots[d.Current] = s
	}
}
