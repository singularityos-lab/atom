package unit

import (
	"strings"
)

// Type is the kind of a unit, derived from the file-name suffix.
type Type string

const (
	TypeService Type = "service"
	TypeTarget  Type = "target"
	TypeSocket  Type = "socket"
	TypeTimer   Type = "timer"
	TypePath    Type = "path"
	TypeMount   Type = "mount"
	TypeSlice   Type = "slice"
	TypeScope   Type = "scope"
	TypeUnknown Type = ""
)

// typeFromName returns the unit type for a unit file name (e.g. "foo.service").
func typeFromName(name string) Type {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return TypeUnknown
	}
	switch Type(name[i+1:]) {
	case TypeService:
		return TypeService
	case TypeTarget:
		return TypeTarget
	case TypeSocket:
		return TypeSocket
	case TypeTimer:
		return TypeTimer
	case TypePath:
		return TypePath
	case TypeMount:
		return TypeMount
	case TypeSlice:
		return TypeSlice
	case TypeScope:
		return TypeScope
	default:
		return TypeUnknown
	}
}

type entry struct {
	key string
	val string
}

// Section is one [Header] block. Entries keep file order; a key may repeat
// (list-append semantics) and an empty value resets the accumulated list.
type Section struct {
	Name    string
	entries []entry
}

func (s *Section) add(key, val string) {
	s.entries = append(s.entries, entry{key: key, val: val})
}

// File is a parsed unit. It is built by Parse and refined by drop-in merges,
// specifier resolution, and the loader.
type File struct {
	// Name is the full unit name, e.g. "getty@tty1.service".
	Name string
	// Type is derived from Name's suffix.
	Type Type
	// Prefix and Instance come from a templated name "prefix@instance.type".
	// For a non-instantiated unit Instance is empty.
	Prefix   string
	Instance string

	// Path is the file the fragment was loaded from, if any.
	Path string

	order    []string
	sections map[string]*Section

	// Warnings records non-fatal parse issues (unknown lines, etc.).
	Warnings []string
}

func newFile(name string) *File {
	prefix, instance, _ := SplitName(name)
	return &File{
		Name:     name,
		Type:     typeFromName(name),
		Prefix:   prefix,
		Instance: instance,
		sections: map[string]*Section{},
	}
}

// section returns the named section, creating it (and recording order) if new.
// Re-entering an existing section name appends to it, matching systemd.
func (f *File) section(name string) *Section {
	if s, ok := f.sections[name]; ok {
		return s
	}
	s := &Section{Name: name}
	f.sections[name] = s
	f.order = append(f.order, name)
	return s
}

// Sections returns section names in first-seen order.
func (f *File) Sections() []string {
	out := make([]string, len(f.order))
	copy(out, f.order)
	return out
}

// HasSection reports whether the file contains the named section.
func (f *File) HasSection(name string) bool {
	_, ok := f.sections[name]
	return ok
}
