package pid1

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteInstalledMarker(t *testing.T) {
	dir := t.TempDir()
	origVar, origInst := atomVarMarker, installedMarker
	defer func() { atomVarMarker, installedMarker = origVar, origInst }()
	atomVarMarker = filepath.Join(dir, "var-atom-var")
	installedMarker = filepath.Join(dir, "run-atom-installed")

	// live: no /var/.atom-var -> no marker
	writeInstalledMarker()
	if _, err := os.Stat(installedMarker); !os.IsNotExist(err) {
		t.Fatal("live boot must not write the installed marker")
	}
	// installed: /var/.atom-var present -> marker written
	os.WriteFile(atomVarMarker, nil, 0o644)
	writeInstalledMarker()
	if _, err := os.Stat(installedMarker); err != nil {
		t.Fatalf("installed system must write the marker: %v", err)
	}
}
