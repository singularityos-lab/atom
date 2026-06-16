package deploy

import (
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	d := NewSingleSlot("9f2b", "2026.06.30")
	path := filepath.Join(t.TempDir(), "lib", "atom", "deployment.json")
	if err := d.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.SchemaVersion != SchemaVersion || got.Current != "a" || got.Next != "a" {
		t.Errorf("decoded wrong: %+v", got)
	}
	if s := got.Slots["a"]; s.Roothash != "9f2b" || s.Version != "2026.06.30" {
		t.Errorf("slot a = %+v", s)
	}
}

func TestConfirmRestoresAttemptsAndHealth(t *testing.T) {
	d := NewSingleSlot("h", "v")
	d.BeginBoot()
	if d.Boot.AttemptsRemaining != 2 || d.Boot.Confirmed {
		t.Fatalf("after BeginBoot: %+v", d.Boot)
	}
	d.Confirm("2026-06-30T12:00:07Z")
	if !d.Boot.Confirmed || d.Boot.AttemptsRemaining != d.Boot.MaxAttempts {
		t.Errorf("after Confirm: %+v", d.Boot)
	}
	if d.Slots["a"].Health != Good {
		t.Errorf("current slot health = %s, want good", d.Slots["a"].Health)
	}
	if d.Boot.LastConfirmedAt == "" {
		t.Error("LastConfirmedAt not set")
	}
}

func TestShouldRollback(t *testing.T) {
	d := NewSingleSlot("h", "v")
	d.Boot.MaxAttempts = 2
	d.Boot.AttemptsRemaining = 2

	// Two unconfirmed boots exhaust the counter.
	d.BeginBoot()
	if d.ShouldRollback() {
		t.Error("should not roll back after 1 unconfirmed boot of 2")
	}
	d.BeginBoot()
	if !d.ShouldRollback() {
		t.Error("should roll back after attempts exhausted with no confirm")
	}

	// A confirm clears the rollback condition.
	d.Confirm("now")
	if d.ShouldRollback() {
		t.Error("confirmed boot must not roll back")
	}
}

func TestLoadMissing(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("expected error loading missing file")
	}
}
