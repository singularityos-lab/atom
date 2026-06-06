package unit

import (
	"strconv"
	"strings"
)

// SplitName splits a unit name into prefix, instance, and type. For a template
// file ("foo@.service") instance is empty; for an instance ("foo@bar.service")
// instance is "bar"; for a plain unit ("foo.service") instance is empty and
// prefix is "foo".
func SplitName(name string) (prefix, instance, typ string) {
	stem := name
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		stem = name[:i]
		typ = name[i+1:]
	}
	if at := strings.IndexByte(stem, '@'); at >= 0 {
		return stem[:at], stem[at+1:], typ
	}
	return stem, "", typ
}

// Specifiers holds the values substituted for %-specifiers in a unit file.
type Specifiers struct {
	FullName   string // %n
	Prefix     string // %p
	Instance   string // %i
	Machine    string // %m  (machine-id)
	Boot       string // %b  (boot-id)
	Host       string // %H  (hostname)
	Home       string // %h
	User       string // %u
	UID        string // %U
	RuntimeDir string // %t  (defaults to /run)
}

// SpecifiersFor derives the name-based specifiers from a unit, leaving the
// host/identity fields for the caller to populate.
func SpecifiersFor(f *File) Specifiers {
	rt := "/run"
	return Specifiers{
		FullName:   f.Name,
		Prefix:     f.Prefix,
		Instance:   f.Instance,
		RuntimeDir: rt,
	}
}

// ResolveSpecifiers expands %-specifiers in every value in place.
func (f *File) ResolveSpecifiers(sp Specifiers) {
	for _, name := range f.order {
		s := f.sections[name]
		for i := range s.entries {
			s.entries[i].val = expandSpecifiers(s.entries[i].val, sp)
		}
	}
}

func expandSpecifiers(v string, sp Specifiers) string {
	if !strings.ContainsRune(v, '%') {
		return v
	}
	var b strings.Builder
	b.Grow(len(v))
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c != '%' || i+1 >= len(v) {
			b.WriteByte(c)
			continue
		}
		i++
		switch v[i] {
		case '%':
			b.WriteByte('%')
		case 'n':
			b.WriteString(sp.FullName)
		case 'N':
			b.WriteString(UnescapeName(sp.FullName))
		case 'p':
			b.WriteString(sp.Prefix)
		case 'P':
			b.WriteString(UnescapeName(sp.Prefix))
		case 'i':
			b.WriteString(sp.Instance)
		case 'I':
			b.WriteString(UnescapeName(sp.Instance))
		case 'f':
			if sp.Instance != "" {
				b.WriteString("/" + UnescapeName(sp.Instance))
			} else {
				b.WriteString("/" + UnescapeName(sp.Prefix))
			}
		case 't':
			b.WriteString(orDefault(sp.RuntimeDir, "/run"))
		case 'h':
			b.WriteString(orDefault(sp.Home, "/root"))
		case 'H':
			b.WriteString(sp.Host)
		case 'm':
			b.WriteString(sp.Machine)
		case 'b':
			b.WriteString(sp.Boot)
		case 'u':
			b.WriteString(orDefault(sp.User, "root"))
		case 'U':
			b.WriteString(orDefault(sp.UID, "0"))
		default:
			// Unknown specifier: keep it verbatim so nothing is silently lost.
			b.WriteByte('%')
			b.WriteByte(v[i])
		}
	}
	return b.String()
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// UnescapeName reverses systemd unit-name escaping: '-' becomes '/', and
// "\xNN" hex escapes become their byte. Unrecognized escapes are kept verbatim.
func UnescapeName(s string) string {
	if !strings.ContainsAny(s, "-\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '-':
			b.WriteByte('/')
		case c == '\\' && i+3 < len(s) && s[i+1] == 'x':
			n, err := strconv.ParseUint(s[i+2:i+4], 16, 8)
			if err != nil {
				b.WriteByte(c)
				continue
			}
			b.WriteByte(byte(n))
			i += 3
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// EscapeName applies systemd unit-name escaping to a path-like string: '/'
// becomes '-', and bytes outside [A-Za-z0-9:_.] become "\xNN". A leading dot
// is escaped as well.
func EscapeName(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '/':
			b.WriteByte('-')
		case c == '_' || c == '.' || c == ':' ||
			c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			if c == '.' && i == 0 {
				b.WriteString("\\x2e")
				continue
			}
			b.WriteByte(c)
		default:
			b.WriteString("\\x")
			const hex = "0123456789abcdef"
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
	return b.String()
}
