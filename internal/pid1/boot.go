package pid1

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/singularityos-lab/atom/internal/cgroup"
	"github.com/singularityos-lab/atom/internal/control"
	"github.com/singularityos-lab/atom/internal/core"
	"github.com/singularityos-lab/atom/internal/logd"
	"github.com/singularityos-lab/atom/internal/reaper"
	"github.com/singularityos-lab/atom/internal/service"
	"github.com/singularityos-lab/atom/internal/unit"
)

// Main is the init entry point. It refuses to run as anything but PID 1 unless
// --force is given (the qemu harness boots it as real PID 1, so the guard is a
// safety net against accidentally running it on a live host).
func Main(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	target := fs.String("target", "default.target", "boot target")
	force := fs.Bool("force", false, "run the boot sequence even when not PID 1")
	exitAfter := fs.Bool("exit-after-target", false, "power off as soon as the target is reached (for CI boot tests)")
	unitDir := fs.String("unit-dir", "", "extra unit dir (highest precedence)")
	noDefaults := fs.Bool("no-default-paths", false, "search only --unit-dir, not the default unit paths")
	debug := fs.Bool("debug", false, "log per-unit start/active/failed progress")
	defStartTimeout := fs.Duration("default-start-timeout", 0, "override the default per-unit start timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *defStartTimeout > 0 {
		service.DefaultStartTimeout = *defStartTimeout
	}

	if os.Getpid() != 1 && !*force {
		fmt.Fprintln(os.Stderr, "atom init: refusing to run as init: not PID 1 (use --force only in a VM/test)")
		return 1
	}

	return boot(bootConfig{
		target:     *target, // resolved in boot() after /proc is mounted
		exitAfter:  *exitAfter,
		unitDir:    *unitDir,
		noDefaults: *noDefaults,
		debug:      *debug,
	})
}

// resolveTarget picks the boot target: an explicit non-default --target wins;
// otherwise the kernel cmdline (atom.unit= or systemd.unit=) is consulted; else
// default.target (which the unit tree usually symlinks to graphical.target).
func resolveTarget(flagTarget string) string {
	if flagTarget != "" && flagTarget != "default.target" {
		return flagTarget
	}
	if t := cmdlineUnit(); t != "" {
		return t
	}
	return "default.target"
}

func cmdlineUnit() string {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return ""
	}
	for _, tok := range strings.Fields(string(data)) {
		// sinit.unit= is primary; atom.unit=/systemd.unit= are accepted for
		// compatibility with existing image bakes.
		for _, pfx := range []string{"sinit.unit=", "atom.unit=", "systemd.unit="} {
			if v, ok := strings.CutPrefix(tok, pfx); ok {
				return v
			}
		}
	}
	return ""
}

type bootConfig struct {
	target     string
	exitAfter  bool
	unitDir    string
	noDefaults bool
	debug      bool
}

// cmdlineValue returns the value of a key=value token on the kernel cmdline.
func cmdlineValue(key string) string {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return ""
	}
	for _, t := range strings.Fields(string(data)) {
		if v, ok := strings.CutPrefix(t, key+"="); ok {
			return v
		}
	}
	return ""
}

// cmdlineHas reports whether the kernel cmdline contains the exact token.
func cmdlineHas(token string) bool {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return false
	}
	for _, t := range strings.Fields(string(data)) {
		if t == token {
			return true
		}
	}
	return false
}

// bootLogPath is the on-device live boot log: a plain-text, append-only file on
// tmpfs that a client (e.g. the splash debug overlay) can tail. It is always
// written, independent of `quiet`, so the on-screen debug toggle can show the
// logs even when the console stays clean. A var so tests can redirect it.
// bootLogPath is the tailable file sink; kmsgPath is the kernel ring buffer. sinit
// writes its boot lines to BOTH: the file is a persistent tmpfs tail, and /dev/kmsg
// means the splash debug overlay (which already tails kmsg) shows the atom[1] lines
// for free, no second source. Both are vars so tests can redirect/disable them.
var (
	bootLogPath = "/run/atom/boot.log"
	kmsgPath    = "/dev/kmsg"
)

var (
	logQuiet bool     // set from the `quiet` kernel cmdline token
	logSink  *os.File // the tailable boot log, opened once /run is mounted
	kmsgSink *os.File // the kernel ring buffer, for the debug overlay
	logBuf   []string // early lines captured before the sinks are open
)

// openLogSink opens the sinks (file + kmsg) and flushes any lines logged before
// they existed. Called once, right after the API filesystems (/run, /dev) mount.
func openLogSink() {
	_ = os.MkdirAll(filepath.Dir(bootLogPath), 0o755)
	if f, err := os.OpenFile(bootLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		logSink = f
	}
	if kmsgPath != "" {
		if kf, err := os.OpenFile(kmsgPath, os.O_WRONLY, 0); err == nil {
			kmsgSink = kf
		}
	}
	for _, l := range logBuf {
		writeSinks(l)
	}
	logBuf = nil
}

// Marker paths (vars so tests can redirect them). atomVarMarker is the persistent
// marker the installer writes on a real /var; installedMarker is the per-boot
// signal sinit hands to atomd.
var atomVarMarker = "/var/.atom-var"
var installedMarker = "/run/atom/installed"

// writeInstalledMarker tells atomd whether this is an installed system or a live
// boot. The initramfs cannot signal it (sinit remounts /run tmpfs and clobbers
// anything under it), so sinit derives it here from the persistent /var/.atom-var:
// present only when the initramfs mounted a real, same-disk /var. A live boot has
// tmpfs /var without it, so the marker stays absent and OTA stays inert. Written
// after /run/atom exists, so it survives the initramfs->sinit /run handoff.
func writeInstalledMarker() {
	if _, err := os.Stat(atomVarMarker); err != nil {
		logf("live/uninstalled: %s absent, OTA inert", atomVarMarker)
		return
	}
	if err := os.WriteFile(installedMarker, nil, 0o644); err != nil {
		logf("installed marker %s: %v", installedMarker, err)
		return
	}
	logf("installed system: wrote %s", installedMarker)
}

func writeSinks(line string) {
	if logSink != nil {
		fmt.Fprintln(logSink, line)
	}
	if kmsgSink != nil {
		fmt.Fprintln(kmsgSink, line)
	}
}

// logf records a boot line. It ALWAYS goes to the sinks (buffered until they open),
// and is echoed to the console only when `quiet` is not set, so the screen stays
// clean while the overlay, serial and the file still see everything.
func logf(format string, a ...any) {
	line := "atom[1]: " + fmt.Sprintf(format, a...)
	if logSink == nil && kmsgSink == nil {
		logBuf = append(logBuf, line)
	} else {
		writeSinks(line)
	}
	if !logQuiet {
		fmt.Println(line)
	}
}

func boot(cfg bootConfig) int {
	// Honor `quiet`: keep the screen clean (no boot text on the console). The
	// tailable sink at bootLogPath still records everything for serial/overlay.
	logQuiet = cmdlineHas("quiet")

	// Disable Ctrl-Alt-Del triggering an immediate kernel reboot; PID 1 owns it.
	_ = syscall.Reboot(syscall.LINUX_REBOOT_CMD_CAD_OFF)

	// Give spawned services systemd's default exec PATH. atom inherits no PATH as
	// init, so bare-name ExecStart (udevadm, systemd-tmpfiles, agetty) would fail
	// to resolve -- exec.Command resolves argv[0] against THIS process's PATH.
	_ = os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	// Mount /proc first so the cmdline and the mount table are readable, then
	// resolve the boot target from the kernel cmdline (atom.unit=).
	mountProc(logf)
	cfg.target = resolveTarget(cfg.target)
	if cmdlineHas("sinit.debug=1") || cmdlineHas("atom.debug=1") {
		cfg.debug = true
	}
	dst := cmdlineValue("sinit.default-start-timeout")
	if dst == "" {
		dst = cmdlineValue("atom.default-start-timeout")
	}
	if v := dst; v != "" {
		if d, ok := unit.ParseTimeSpan(v); ok && d > 0 {
			service.DefaultStartTimeout = d
			logf("default start timeout = %s (from cmdline)", d)
		}
	}
	logf("booting, target=%s", cfg.target)

	mountAPIFilesystems(logf)
	openLogSink()          // /run is up now: open the tailable boot log, flush early lines
	writeInstalledMarker() // installed-vs-live signal for atomd (fail-safe: absent = live)
	setupCgroupHierarchy()
	seedMachineID(logf)

	reg := reaper.NewRegistry()
	service.ReaperWait = reg.Wait
	rp := reaper.NewReaper(reg)
	go rp.Run()
	defer rp.Stop()

	var paths []string
	if cfg.unitDir != "" {
		paths = append(paths, cfg.unitDir)
	}
	if !cfg.noDefaults {
		paths = append(paths, unit.DefaultSearchPath...)
	}
	loader := &unit.Loader{Paths: paths}

	m, err := core.Build(loader, cfg.target)
	if err != nil {
		logf("FATAL: build transaction for %s: %v", cfg.target, err)
		return emergency(cfg)
	}
	m.AttachLogs(logd.NewRegistry(1000))
	m.OnUnitError = func(name string, err error) { logf("unit %s failed: %v", name, err) }
	m.OnUnitStop = func(name, reason string) { logf("stopped %s (%s)", name, reason) }
	if cfg.debug {
		m.OnUnitStart = func(n string) { logf("starting %s", n) }
		m.OnUnitActive = func(n string) { logf("active %s", n) }
	}
	if miss := m.Missing(); len(miss) > 0 {
		logf("not found (enabled but no unit file), skipped: %s", strings.Join(miss, " "))
	}

	// Bring the control socket up BEFORE starting the target, so even a wedged
	// boot stays introspectable via 'atom ctl list-units'.
	if srv, err := control.Listen(control.DefaultSocket, m); err != nil {
		logf("control socket: %v", err)
	} else {
		go srv.Serve()
		defer srv.Close()
		logf("control socket at %s", control.DefaultSocket)
	}

	plan := m.Plan(cfg.target)
	logf("transaction: %d units, %d layers", len(plan.Units), len(plan.Layers))

	start := time.Now()
	if err := m.StartTarget(context.Background(), cfg.target); err != nil {
		logf("ERROR starting %s: %v", cfg.target, err)
	}
	logf("reached %s in %s", cfg.target, time.Since(start).Round(time.Millisecond))

	if cfg.exitAfter {
		logf("exit-after-target set: shutting down")
		shutdown(m, cfg.target)
		return powerOff()
	}

	waitForShutdown(m, cfg.target)
	return 0
}

// setupCgroupHierarchy creates the top-level slices.
func setupCgroupHierarchy() {
	cg := cgroup.New()
	for _, slice := range []string{"system.slice", "user.slice", "machine.slice"} {
		if err := cg.Create(slice); err != nil {
			logf("cgroup %s: %v", slice, err)
		}
	}
}

// waitForShutdown blocks until a shutdown signal, then stops and powers off or
// reboots. SIGTERM/SIGINT => power off; SIGINT from Ctrl-Alt-Del (the kernel
// sends SIGINT to PID 1 after CAD_OFF) is treated as reboot via the keyword.
func waitForShutdown(m *core.Manager, target string) {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGUSR1)
	sig := <-ch
	logf("got signal %v, shutting down", sig)
	shutdown(m, target)
	switch sig {
	case syscall.SIGINT:
		reboot()
	default:
		powerOff()
	}
}

// emergency is the last-resort path when the boot transaction cannot even be built.
// It gives the operator an interactive rescue shell on the console instead of hanging
// PID 1 forever (a bare select{} would need a hard power cycle to recover). If no
// console is usable (headless / CI), it powers off after a few failed spawns so the
// box never spins wedged.
func emergency(cfg bootConfig) int {
	logf("emergency: cannot build the boot transaction; starting a rescue shell")
	if cfg.exitAfter {
		return powerOff()
	}
	fails := 0
	for fails < 5 {
		sh := exec.Command("/bin/sh")
		sh.Stdin, sh.Stdout, sh.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := sh.Run(); err != nil {
			logf("emergency shell: %v", err)
			fails++
			time.Sleep(time.Second)
			continue
		}
		fails = 0 // a clean shell exit: respawn a fresh one for the operator
	}
	return powerOff()
}
