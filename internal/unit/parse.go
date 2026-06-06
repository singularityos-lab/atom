package unit

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Parse reads one unit fragment. The returned error is only for I/O failures;
// content problems are accumulated in File.Warnings (the parser is total).
func Parse(name string, r io.Reader) (*File, error) {
	f := newFile(name)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var cur *Section
	// continuation state
	var contKey string
	var contBuf strings.Builder
	contActive := false

	lineno := 0
	for sc.Scan() {
		lineno++
		line := sc.Text()
		line = strings.TrimRight(line, "\r")

		if contActive {
			seg := strings.TrimRight(line, " \t")
			if strings.HasSuffix(seg, "\\") {
				contBuf.WriteString(strings.TrimSuffix(seg, "\\"))
				contBuf.WriteByte(' ')
				continue
			}
			contBuf.WriteString(line)
			cur.add(contKey, strings.TrimSpace(contBuf.String()))
			contActive = false
			contBuf.Reset()
			continue
		}

		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") {
			continue
		}

		if strings.HasPrefix(s, "[") {
			if strings.HasSuffix(s, "]") {
				cur = f.section(s[1 : len(s)-1])
			} else {
				f.warnf("line %d: malformed section header %q", lineno, s)
			}
			continue
		}

		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			f.warnf("line %d: not a key=value line: %q", lineno, s)
			continue
		}
		if cur == nil {
			f.warnf("line %d: assignment before any section: %q", lineno, s)
			continue
		}

		key := strings.TrimSpace(s[:eq])
		val := strings.TrimSpace(s[eq+1:])

		if strings.HasSuffix(val, "\\") {
			contKey = key
			contBuf.Reset()
			contBuf.WriteString(strings.TrimSuffix(val, "\\"))
			contBuf.WriteByte(' ')
			contActive = true
			continue
		}
		cur.add(key, val)
	}

	if contActive && cur != nil {
		cur.add(contKey, strings.TrimSpace(contBuf.String()))
	}
	if err := sc.Err(); err != nil {
		return f, fmt.Errorf("unit %s: %w", name, err)
	}
	return f, nil
}

// ParseFile reads and parses a unit fragment from disk, setting File.Path.
func ParseFile(path string) (*File, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()

	name := baseName(path)
	f, err := Parse(name, fh)
	if err != nil {
		return nil, err
	}
	f.Path = path
	return f, nil
}

func (f *File) warnf(format string, a ...any) {
	f.Warnings = append(f.Warnings, fmt.Sprintf(format, a...))
}

func baseName(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}
