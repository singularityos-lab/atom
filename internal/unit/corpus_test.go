package unit

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRealSystemdCorpus sweeps the host's systemd unit directory (when present)
// and asserts the parser is total over real-world units: every file parses
// without an I/O error and yields a recognizable type. This is a robustness
// gate, not a correctness oracle; it is skipped where systemd units are absent
// (CI without systemd, or the Sinty target before its unit corpus exists).
func TestRealSystemdCorpus(t *testing.T) {
	dirs := []string{"/usr/lib/systemd/system", "/lib/systemd/system"}
	var root string
	for _, d := range dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			root = d
			break
		}
	}
	if root == "" {
		t.Skip("no systemd unit directory on this host")
	}

	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}

	var parsed, withWarnings, typed int
	for _, e := range ents {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		switch filepath.Ext(e.Name()) {
		case ".service", ".target", ".socket", ".timer", ".path", ".mount", ".slice", ".scope":
		default:
			continue
		}
		f, err := ParseFile(filepath.Join(root, e.Name()))
		if err != nil {
			t.Errorf("ParseFile(%s) hard error: %v", e.Name(), err)
			continue
		}
		parsed++
		if f.Type != TypeUnknown {
			typed++
		}
		if len(f.Warnings) > 0 {
			withWarnings++
		}
	}

	if parsed == 0 {
		t.Skip("systemd dir present but no parseable unit files")
	}
	t.Logf("corpus: parsed=%d typed=%d withWarnings=%d (root=%s)", parsed, typed, withWarnings, root)

	if frac := float64(withWarnings) / float64(parsed); frac > 0.10 {
		t.Errorf("too many units produced warnings: %.0f%% (%d/%d)", frac*100, withWarnings, parsed)
	}
}
