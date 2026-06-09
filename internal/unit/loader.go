package unit

import (
	"os"
	"path/filepath"
	"sort"
)

// DefaultSearchPath is the unit lookup path in DESCENDING precedence: an entry
// earlier in the slice overrides a later one. It includes the native atom
// locations plus the systemd locations, so an existing systemd corpus is found
// unchanged during the transition.
var DefaultSearchPath = []string{
	"/etc/atom/system",
	"/run/atom/system",
	"/usr/lib/atom/system",
	"/etc/systemd/system",
	"/run/systemd/system",
	"/usr/lib/systemd/system",
	"/lib/systemd/system",
}

// Loader resolves and assembles units from a search path: it finds the
// fragment (following templates), applies drop-ins in precedence order, and
// resolves specifiers.
type Loader struct {
	// Paths is the search path in descending precedence. If nil,
	// DefaultSearchPath is used.
	Paths []string

	// Identity, if set, fills the host/identity specifiers (%H, %h, %m, ...)
	// that are not derivable from the unit name alone.
	Identity Specifiers
}

func (l *Loader) paths() []string {
	if l.Paths != nil {
		return l.Paths
	}
	return DefaultSearchPath
}

// Load resolves the named unit: it locates the fragment (or its template),
// merges drop-ins, and expands specifiers. A unit with no fragment but with
// drop-ins (or a pure synthesized target) yields an empty-but-valid File.
func (l *Loader) Load(name string) (*File, error) {
	path, _ := l.findFragment(name)

	var f *File
	if path == "" {
		f = newFile(name)
	} else {
		fh, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		f, err = Parse(name, fh)
		fh.Close()
		if err != nil {
			return nil, err
		}
		f.Path = path
	}

	for _, dir := range l.dropinDirs(name) {
		l.applyDropins(f, dir)
	}

	sp := l.specifiersFor(f)
	f.ResolveSpecifiers(sp)
	return f, nil
}

// findFragment returns the highest-precedence fragment file for name, falling
// back to the unit's template ("prefix@.type") for an instance name.
func (l *Loader) findFragment(name string) (path string, isTemplate bool) {
	for _, p := range l.paths() {
		full := filepath.Join(p, name)
		if fileExists(full) {
			return full, false
		}
	}
	prefix, instance, typ := SplitName(name)
	if instance != "" && prefix != "" && typ != "" {
		tmpl := prefix + "@." + typ
		for _, p := range l.paths() {
			full := filepath.Join(p, tmpl)
			if fileExists(full) {
				return full, true
			}
		}
	}
	return "", false
}

// dropinDirs returns the drop-in directories to apply, from lowest to highest
// precedence (so a later one overrides an earlier one). For an instance, the
// template drop-in dir is applied before the instance-specific one.
func (l *Loader) dropinDirs(name string) []string {
	prefix, instance, typ := SplitName(name)
	var dirs []string
	// Paths are descending precedence; iterate reversed so lowest applies first.
	ps := l.paths()
	for i := len(ps) - 1; i >= 0; i-- {
		p := ps[i]
		if instance != "" && prefix != "" && typ != "" {
			dirs = append(dirs, filepath.Join(p, prefix+"@."+typ+".d"))
		}
		dirs = append(dirs, filepath.Join(p, name+".d"))
	}
	return dirs
}

// applyDropins merges every *.conf in dir (sorted) into f.
func (l *Loader) applyDropins(f *File, dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var confs []string
	for _, e := range ents {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".conf" {
			confs = append(confs, e.Name())
		}
	}
	sort.Strings(confs)
	for _, c := range confs {
		df, err := ParseFile(filepath.Join(dir, c))
		if err != nil {
			continue
		}
		f.Merge(df)
	}
}

// Merge appends every section/entry from other onto f. Because access uses
// last-wins for scalars and list-append for lists (with empty-value reset),
// appending a drop-in's entries yields systemd override semantics.
func (f *File) Merge(other *File) {
	for _, name := range other.order {
		src := other.sections[name]
		dst := f.section(name)
		dst.entries = append(dst.entries, src.entries...)
	}
	f.Warnings = append(f.Warnings, other.Warnings...)
}

func (l *Loader) specifiersFor(f *File) Specifiers {
	sp := SpecifiersFor(f)
	id := l.Identity
	if id.Host != "" {
		sp.Host = id.Host
	}
	if id.Home != "" {
		sp.Home = id.Home
	}
	if id.Machine != "" {
		sp.Machine = id.Machine
	}
	if id.Boot != "" {
		sp.Boot = id.Boot
	}
	if id.User != "" {
		sp.User = id.User
	}
	if id.UID != "" {
		sp.UID = id.UID
	}
	if id.RuntimeDir != "" {
		sp.RuntimeDir = id.RuntimeDir
	}
	return sp
}

// EnabledDeps returns the units linked into <name>.wants/ and <name>.requires/
// across the search path. This is the `systemctl enable` mechanism: a symlink in
// these directories is equivalent to a Wants=/Requires= on the target, and is
// how most services are pulled into a boot target.
func (l *Loader) EnabledDeps(name string) (wants, requires []string) {
	return l.linksIn(name + ".wants"), l.linksIn(name + ".requires")
}

func (l *Loader) linksIn(dirName string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range l.paths() {
		ents, err := os.ReadDir(filepath.Join(p, dirName))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			if n := e.Name(); !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
