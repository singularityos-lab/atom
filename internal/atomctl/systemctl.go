package atomctl

import (
	"fmt"
	"os"
	"strings"
)

// systemctl compatibility shim.
//
// After systemd is removed there is no systemctl, but muscle memory (and the
// odd script) still reaches for it. This maps a practical subset of systemctl
// onto atomctl's real verbs, talking to the running init over the same control
// socket. It is a genuine alias, not a systemd stub: there is no systemd here,
// and where the two models differ we say so rather than pretend.
//
// `--user` has no target under sinit by design (the per-user session bus is
// dbus's job, and there is no user service manager), so it is a clean no-op:
// callers that "start" a user unit get success instead of a hang or a failure.

type sctlAction struct {
	atomctlArgs []string // when non-nil, run atomctl.Main with these
	exit        int      // used only when atomctlArgs is nil
	message     string   // printed when atomctlArgs is nil (stdout if exit==0, else stderr)
}

// translateSystemctl maps a systemctl invocation to an atomctl argv, or decides
// to short-circuit. Pure and testable: it performs no I/O and reaches no socket.
func translateSystemctl(args []string) sctlAction {
	user := false
	var pos []string
	for _, a := range args {
		switch {
		case a == "--user":
			user = true
		case a == "--system":
			// default scope; ignore
		case strings.HasPrefix(a, "-"):
			// systemd-only flags (--no-pager, --no-legend, -q, --now, ...): ignore
		default:
			pos = append(pos, a)
		}
	}

	if len(pos) == 0 {
		return sctlAction{exit: 2, message: "systemctl (sinit shim): usage: systemctl [--user] <verb> [unit]"}
	}
	verb := pos[0]
	unit := ""
	if len(pos) > 1 {
		unit = pos[1]
	}

	if user {
		return sctlAction{exit: 0, message: fmt.Sprintf(
			"systemctl --user %s: no user service manager under sinit (the session bus is dbus's job); no-op", verb)}
	}

	withUnit := func(v string) []string {
		if unit != "" {
			return []string{v, unit}
		}
		return []string{v}
	}

	switch verb {
	case "start", "stop", "restart", "status", "daemon-reload",
		"reboot", "poweroff", "halt", "list-units", "logs", "boot-confirm":
		return sctlAction{atomctlArgs: withUnit(verb)}
	case "is-active":
		return sctlAction{atomctlArgs: []string{"status", unit}}
	case "list-unit-files":
		return sctlAction{atomctlArgs: []string{"list-units"}}
	case "enable", "disable", "is-enabled", "mask", "unmask":
		return sctlAction{exit: 1, message: fmt.Sprintf(
			"systemctl %s: under sinit enablement is the <target>.wants/ symlink set baked in the image, not a live toggle", verb)}
	default:
		// Unknown verb: pass through so atomctl handles or rejects it cleanly.
		return sctlAction{atomctlArgs: withUnit(verb)}
	}
}

// SystemctlShim is the multicall entry for a `systemctl` symlink.
func SystemctlShim(args []string) int {
	a := translateSystemctl(args)
	if a.atomctlArgs != nil {
		return Main(a.atomctlArgs)
	}
	if a.exit == 0 {
		fmt.Println(a.message)
	} else {
		fmt.Fprintln(os.Stderr, a.message)
	}
	return a.exit
}
