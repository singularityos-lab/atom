package dbus

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// SystemBusAddress returns the system bus socket path, honoring
// DBUS_SYSTEM_BUS_ADDRESS (unix:path=... form) and falling back to the
// well-known default.
func SystemBusAddress() string {
	if a := os.Getenv("DBUS_SYSTEM_BUS_ADDRESS"); a != "" {
		if p := parseUnixPath(a); p != "" {
			return p
		}
	}
	return "/run/dbus/system_bus_socket"
}

// parseUnixPath extracts the socket path from a "unix:path=/x,guid=..." address.
func parseUnixPath(addr string) string {
	for _, field := range strings.Split(addr, ",") {
		field = strings.TrimPrefix(field, "unix:")
		if v, ok := strings.CutPrefix(field, "path="); ok {
			return v
		}
	}
	return ""
}

// Conn is an authenticated connection to a message bus.
type Conn struct {
	c      net.Conn
	r      *bufio.Reader
	serial uint32
}

// Dial connects to the system bus, performs SASL EXTERNAL auth, and sends the
// mandatory Hello. The returned Conn is ready for method calls.
func Dial() (*Conn, error) {
	return DialPath(SystemBusAddress())
}

// DialPath is Dial against an explicit socket path (used by tests).
func DialPath(path string) (*Conn, error) {
	c, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	conn := &Conn{c: c, r: bufio.NewReader(c)}
	if err := conn.authExternal(); err != nil {
		c.Close()
		return nil, err
	}
	if _, err := conn.hello(); err != nil {
		c.Close()
		return nil, err
	}
	return conn, nil
}

// Close releases the connection.
func (c *Conn) Close() error { return c.c.Close() }

// authExternal runs the SASL EXTERNAL handshake: a leading NUL, then AUTH with
// the hex-encoded uid, expecting OK, then BEGIN. This is the only mechanism the
// system bus needs from a local root client.
func (c *Conn) authExternal() error {
	uid := fmt.Sprintf("%d", os.Getuid())
	hexUID := make([]byte, 0, len(uid)*2)
	for _, b := range []byte(uid) {
		hexUID = append(hexUID, "0123456789abcdef"[b>>4], "0123456789abcdef"[b&0xf])
	}
	if _, err := c.c.Write([]byte("\x00AUTH EXTERNAL " + string(hexUID) + "\r\n")); err != nil {
		return err
	}
	line, err := c.readLine()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "OK ") {
		return fmt.Errorf("dbus: auth rejected: %q", line)
	}
	if _, err := c.c.Write([]byte("BEGIN\r\n")); err != nil {
		return err
	}
	return nil
}

func (c *Conn) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// enc is a little-endian marshalling buffer that tracks alignment relative to
// the start of the message, as the D-Bus wire format requires.
type enc struct{ b []byte }

func (e *enc) align(n int) {
	for len(e.b)%n != 0 {
		e.b = append(e.b, 0)
	}
}
func (e *enc) byte(v byte)   { e.b = append(e.b, v) }
func (e *enc) u32(v uint32)  { e.align(4); e.b = binary.LittleEndian.AppendUint32(e.b, v) }
func (e *enc) str(s string)  { e.u32(uint32(len(s))); e.b = append(e.b, s...); e.b = append(e.b, 0) }
func (e *enc) sig(s string)  { e.byte(byte(len(s))); e.b = append(e.b, s...); e.b = append(e.b, 0) }

// call issues a METHOD_CALL and returns the reply body plus its signature.
// argSig/args currently support only a single string argument ("s") or none
// (""), which is all NameHasOwner/Hello need.
func (c *Conn) call(dest, path, iface, member, argSig string, arg string) (sig string, body []byte, err error) {
	c.serial++
	serial := c.serial

	// Body first, so we know its length for the header.
	var bodyEnc enc
	if argSig == "s" {
		bodyEnc.str(arg)
	}

	var h enc
	h.byte('l')                 // little-endian
	h.byte(1)                   // METHOD_CALL
	h.byte(0)                   // flags
	h.byte(1)                   // protocol version
	h.u32(uint32(len(bodyEnc.b))) // body length
	h.u32(serial)

	// Header fields: ARRAY of (BYTE code, VARIANT value).
	fields := []struct {
		code byte
		typ  byte // 's' string, 'o' object path, 'g' signature
		val  string
	}{
		{1, 'o', path},
		{2, 's', iface},
		{3, 's', member},
		{6, 's', dest},
	}
	if argSig != "" {
		fields = append(fields, struct {
			code byte
			typ  byte
			val  string
		}{8, 'g', argSig})
	}

	var fe enc
	for _, f := range fields {
		fe.align(8) // each STRUCT in the array is 8-aligned
		fe.byte(f.code)
		fe.sig(string(f.typ)) // variant signature
		switch f.typ {
		case 'g':
			fe.sig(f.val)
		default: // 's' and 'o' marshal identically
			fe.str(f.val)
		}
	}
	h.u32(uint32(len(fe.b))) // array length in bytes
	h.b = append(h.b, fe.b...)
	h.align(8) // body starts 8-aligned

	msg := append(h.b, bodyEnc.b...)
	if _, err := c.c.Write(msg); err != nil {
		return "", nil, err
	}
	return c.readReply(serial)
}

// readReply reads messages until the METHOD_RETURN or ERROR matching serial.
func (c *Conn) readReply(serial uint32) (string, []byte, error) {
	for {
		msgType, replySerial, bodySig, body, err := c.readMessage()
		if err != nil {
			return "", nil, err
		}
		if replySerial != serial {
			continue // a signal or an unrelated reply; skip
		}
		if msgType == 3 { // ERROR
			return "", nil, fmt.Errorf("dbus: error reply")
		}
		return bodySig, body, nil
	}
}

// readMessage reads one full message and returns its type, reply serial (0 if
// none), body signature, and body bytes.
func (c *Conn) readMessage() (msgType byte, replySerial uint32, bodySig string, body []byte, err error) {
	fixed := make([]byte, 16)
	if _, err = readFull(c.r, fixed); err != nil {
		return
	}
	if fixed[0] != 'l' {
		err = errors.New("dbus: only little-endian replies supported")
		return
	}
	msgType = fixed[1]
	bodyLen := binary.LittleEndian.Uint32(fixed[4:8])
	fieldsLen := binary.LittleEndian.Uint32(fixed[12:16])

	fieldBytes := make([]byte, fieldsLen)
	if _, err = readFull(c.r, fieldBytes); err != nil {
		return
	}
	// The body is aligned to 8 bytes after the header fields array.
	pad := (8 - int(16+fieldsLen)%8) % 8
	if pad > 0 {
		if _, err = readFull(c.r, make([]byte, pad)); err != nil {
			return
		}
	}
	body = make([]byte, bodyLen)
	if _, err = readFull(c.r, body); err != nil {
		return
	}
	replySerial, bodySig = parseFields(fieldBytes)
	return
}

// parseFields walks the header-field array enough to extract REPLY_SERIAL (5)
// and SIGNATURE (8). It does not need to fully decode every field.
func parseFields(b []byte) (replySerial uint32, sig string) {
	i := 0
	for i < len(b) {
		if i%8 != 0 {
			i += 8 - i%8 // align to next struct
		}
		if i >= len(b) {
			break
		}
		code := b[i]
		i++
		// variant: signature (1-byte len + bytes + nul)
		if i >= len(b) {
			break
		}
		slen := int(b[i])
		i++
		vsig := string(b[i : i+slen])
		i += slen + 1 // + nul
		switch vsig {
		case "u":
			if i%4 != 0 {
				i += 4 - i%4
			}
			v := binary.LittleEndian.Uint32(b[i : i+4])
			i += 4
			if code == 5 {
				replySerial = v
			}
		case "s", "o":
			if i%4 != 0 {
				i += 4 - i%4
			}
			slen := int(binary.LittleEndian.Uint32(b[i : i+4]))
			i += 4
			i += slen + 1
		case "g":
			glen := int(b[i])
			i++
			if code == 8 {
				sig = string(b[i : i+glen])
			}
			i += glen + 1
		default:
			return // unknown field type: stop rather than misalign
		}
	}
	return
}

func (c *Conn) hello() (string, error) {
	sig, body, err := c.call("org.freedesktop.DBus", "/org/freedesktop/DBus",
		"org.freedesktop.DBus", "Hello", "", "")
	if err != nil {
		return "", err
	}
	if sig != "s" || len(body) < 4 {
		return "", errors.New("dbus: malformed Hello reply")
	}
	n := int(binary.LittleEndian.Uint32(body[:4]))
	return string(body[4 : 4+n]), nil
}

// NameHasOwner reports whether name currently has an owner on the bus.
func (c *Conn) NameHasOwner(name string) (bool, error) {
	sig, body, err := c.call("org.freedesktop.DBus", "/org/freedesktop/DBus",
		"org.freedesktop.DBus", "NameHasOwner", "s", name)
	if err != nil {
		return false, err
	}
	if sig != "b" || len(body) < 4 {
		return false, errors.New("dbus: malformed NameHasOwner reply")
	}
	return binary.LittleEndian.Uint32(body[:4]) != 0, nil
}

// WaitForName blocks until name has an owner, ctx is done, or the deadline is
// hit. It polls (D-Bus signal subscription would be leaner, but for a one-shot
// boot readiness check a short poll is simpler and robust). Returns nil once
// the name is owned.
func WaitForName(ctx context.Context, name string, poll time.Duration) error {
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	for {
		// A fresh connection each retry survives the bus not being up yet.
		conn, err := Dial()
		if err == nil {
			owned, cerr := conn.NameHasOwner(name)
			conn.Close()
			if cerr == nil && owned {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	got := 0
	for got < len(buf) {
		n, err := r.Read(buf[got:])
		got += n
		if err != nil {
			return got, err
		}
	}
	return got, nil
}
