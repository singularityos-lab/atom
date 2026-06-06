package unit

import (
	"strconv"
	"strings"
	"time"
)

// rawValues returns the values assigned to key in section, in order, honoring
// list-reset semantics: an empty assignment (key=) clears what came before.
func (f *File) rawValues(section, key string) []string {
	s, ok := f.sections[section]
	if !ok {
		return nil
	}
	var out []string
	for _, e := range s.entries {
		if e.key != key {
			continue
		}
		if e.val == "" {
			out = out[:0]
			continue
		}
		out = append(out, e.val)
	}
	return out
}

// Get returns the last value assigned to key in section ("last wins" for
// scalar directives). ok is false if the key is unset.
func (f *File) Get(section, key string) (val string, ok bool) {
	vs := f.rawValues(section, key)
	if len(vs) == 0 {
		return "", false
	}
	return vs[len(vs)-1], true
}

// GetDefault returns the last value, or def if unset.
func (f *File) GetDefault(section, key, def string) string {
	if v, ok := f.Get(section, key); ok {
		return v
	}
	return def
}

// List returns one entry per assignment line (e.g. each ExecStartPre=),
// honoring reset. Use this for directives that are themselves whole values.
func (f *File) List(section, key string) []string {
	return f.rawValues(section, key)
}

// Strv returns whitespace-split tokens unioned across every assignment of key,
// honoring reset. Use this for space-separated list directives such as After=,
// Wants=, Requires=, Before=, Conflicts=, WantedBy=.
func (f *File) Strv(section, key string) []string {
	var out []string
	for _, v := range f.rawValues(section, key) {
		out = append(out, strings.Fields(v)...)
	}
	return out
}

// Bool parses a boolean directive (systemd spellings), returning def if unset
// or unparseable.
func (f *File) Bool(section, key string, def bool) bool {
	v, ok := f.Get(section, key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "yes", "true", "on":
		return true
	case "0", "no", "false", "off":
		return false
	default:
		return def
	}
}

// Int parses an integer directive, returning def if unset or unparseable.
func (f *File) Int(section, key string, def int64) int64 {
	v, ok := f.Get(section, key)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return def
	}
	return n
}

// Duration parses a systemd time span ("30s", "5min", "1h30s", or a bare
// number meaning seconds), returning def if unset or unparseable. The special
// values "infinity"/"0" yield 0 (meaning "disabled" in systemd semantics; the
// caller decides what 0 means for a given directive).
func (f *File) Duration(section, key string, def time.Duration) time.Duration {
	v, ok := f.Get(section, key)
	if !ok {
		return def
	}
	d, parsed := ParseTimeSpan(v)
	if !parsed {
		return def
	}
	return d
}

// ParseTimeSpan parses a systemd-style time span. It accepts a sum of
// value+unit segments (us, ms, s, sec, m, min, h, d, w) and a bare number
// (seconds). Returns ok=false if nothing parseable was found.
func ParseTimeSpan(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "":
		return 0, false
	case "infinity":
		return 0, true
	}

	// Bare number => seconds.
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(n * float64(time.Second)), true
	}

	units := []struct {
		suffix string
		d      time.Duration
	}{
		{"us", time.Microsecond}, {"ms", time.Millisecond},
		{"sec", time.Second}, {"s", time.Second},
		{"min", time.Minute}, {"m", time.Minute},
		{"h", time.Hour}, {"d", 24 * time.Hour}, {"w", 7 * 24 * time.Hour},
	}

	var total time.Duration
	found := false
	i := 0
	for i < len(s) {
		// skip separators
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		// read number
		start := i
		for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
			i++
		}
		if start == i {
			return 0, false
		}
		num, err := strconv.ParseFloat(s[start:i], 64)
		if err != nil {
			return 0, false
		}
		// read unit
		ustart := i
		for i < len(s) && !(s[i] >= '0' && s[i] <= '9') && s[i] != ' ' && s[i] != '\t' {
			i++
		}
		usuf := strings.ToLower(s[ustart:i])
		matched := false
		for _, u := range units {
			if usuf == u.suffix {
				total += time.Duration(num * float64(u.d))
				matched = true
				found = true
				break
			}
		}
		if !matched {
			return 0, false
		}
	}
	return total, found
}
