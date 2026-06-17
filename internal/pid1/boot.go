package pid1

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/singularityos-lab/atom/internal/cgroup"
	"github.com/singularityos-lab/atom/internal/control"
	"github.com/singularityos-lab/atom/internal/core"
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
	if err := fs.Parse(args); err != nil {
		return 2
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
		if v, ok := strings.CutPrefix(tok, "atom.unit="); ok {
			return v
		}
		if v, ok := strings.CutPrefix(tok, "systemd.unit="); ok {
			return v
		}
	}
	return ""
}

type bootConfig struct {
	target     string
	exitAfter  bool
	unitDir    string
	noDefaults bool
}

func logf(format string, a ...any) {
	fmt.Printf("atom[1]: "+format+"\n", a...)
}

func boot(cfg bootConfig) int {
	// Disable Ctrl-Alt-Del triggering an immediate kernel reboot; PID 1 owns it.
	_ = syscall.Reboot(syscall.LINUX_REBOOT_CMD_CAD_OFF)

	// Mount /proc first so the cmdline and the mount table are readable, then
	// resolve the boot target from the kernel cmdline (atom.unit=).
	mountProc(logf)
	cfg.target = resolveTarget(cfg.target)
	logf("booting, target=%s", cfg.target)

	mountAPIFilesystems(logf)
	setupCgroupHierarchy()

	reaper := NewReaper(nil)
	go reaper.Run()
	defer reaper.Stop()

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
	m.OnUnitError = func(name string, err error) { logf("unit %s failed: %v", name, err) }

	plan := m.Plan(cfg.target)
	logf("transaction: %d units, %d layers", len(plan.Units), len(plan.Layers))

	start := time.Now()
	if err := m.StartTarget(context.Background(), cfg.target); err != nil {
		logf("ERROR starting %s: %v", cfg.target, err)
	}
	logf("reached %s in %s", cfg.target, time.Since(start).Round(time.Millisecond))

	if srv, err := control.Listen(control.DefaultSocket, m); err != nil {
		logf("control socket: %v", err)
	} else {
		go srv.Serve()
		defer srv.Close()
		logf("control socket at %s", control.DefaultSocket)
	}

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

// emergency is the last-resort path when the boot transaction cannot even be
// built; in a real system this would spawn an emergency shell. For now it powers
// off so a CI boot does not hang.
func emergency(cfg bootConfig) int {
	logf("emergency: cannot continue")
	if cfg.exitAfter {
		return powerOff()
	}
	select {} // a real emergency.target would run a shell here
}
