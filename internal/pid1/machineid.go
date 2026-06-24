package pid1

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"syscall"
)

// seedMachineID ensures /etc/machine-id holds a valid id, a PID-1 duty. Programs
// like dbus-daemon abort at startup without one. If the file is empty or
// missing, a random id (systemd-compatible: 32 lowercase hex + newline) is
// generated; on a writable /etc it is written there, on a read-only /etc a
// transient id is written to /run and bind-mounted over /etc/machine-id.
func seedMachineID(log func(string, ...any)) {
	const path = "/etc/machine-id"
	if fileNonEmpty(path) {
		return
	}
	id, err := randomMachineID()
	if err != nil {
		log("machine-id: %v", err)
		return
	}
	content := []byte(id + "\n")

	if err := os.WriteFile(path, content, 0o444); err == nil {
		log("seeded machine-id")
		return
	}
	// Read-only /etc: stage in /run and bind-mount over the (empty) file.
	const runPath = "/run/machine-id"
	if err := os.WriteFile(runPath, content, 0o444); err != nil {
		log("machine-id: write /run: %v", err)
		return
	}
	if _, err := os.Stat(path); err != nil {
		// the mount target must exist; create it if absent
		_ = os.WriteFile(path, nil, 0o444)
	}
	if err := syscall.Mount(runPath, path, "", syscall.MS_BIND, ""); err != nil {
		log("machine-id: bind-mount: %v", err)
		return
	}
	log("seeded transient machine-id (read-only /etc, bind-mounted)")
}

// randomMachineID returns 32 lowercase hex characters (16 random bytes).
func randomMachineID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func fileNonEmpty(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() > 0
}
