package socketact

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Command builds (but does not start) the process that runs a socket-activated
// service with its listening fds passed per the LISTEN_FDS protocol. It works by
// re-execing self as "sd-exec", which sets LISTEN_PID to the final service's pid
// (impossible to set before fork) and then execs the real service. The listener
// fds are inherited via ExtraFiles, landing at fd 3, 4, ... in declaration order.
func Command(self string, listeners []*Listener, svcArgv, env []string) *exec.Cmd {
	n := len(listeners)
	names := make([]string, n)
	for i, l := range listeners {
		names[i] = l.Name
	}
	args := append([]string{"sd-exec", strconv.Itoa(n), strings.Join(names, ":"), "--"}, svcArgv...)
	cmd := exec.Command(self, args...)
	cmd.Env = env
	for _, l := range listeners {
		cmd.ExtraFiles = append(cmd.ExtraFiles, l.File)
	}
	return cmd
}

// SdExec is the re-exec trampoline (atom sd-exec N names -- cmd args...). It
// clears close-on-exec on the inherited listener fds, sets the LISTEN_*
// environment with its own pid, and execs the real service.
func SdExec(args []string) int {
	n, argv, listenEnv, err := sdExecPlan(args, os.Getpid())
	if err != nil {
		fmt.Fprintln(os.Stderr, "sd-exec:", err)
		return 2
	}
	for fd := 3; fd < 3+n; fd++ {
		// clear FD_CLOEXEC so the fd survives the upcoming execve
		_, _, _ = syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_SETFD), 0)
	}
	env := append(os.Environ(), listenEnv...)
	if err := syscall.Exec(argv[0], argv, env); err != nil {
		fmt.Fprintf(os.Stderr, "sd-exec: exec %s: %v\n", argv[0], err)
		return 1
	}
	return 0
}

// sdExecPlan parses the sd-exec arguments and builds the LISTEN_* environment
// for the given pid. Pure (no exec), so it is unit-testable.
func sdExecPlan(args []string, pid int) (n int, argv, listenEnv []string, err error) {
	if len(args) < 3 {
		return 0, nil, nil, fmt.Errorf("usage: sd-exec N names -- cmd [args]")
	}
	n, err = strconv.Atoi(args[0])
	if err != nil {
		return 0, nil, nil, fmt.Errorf("bad fd count %q", args[0])
	}
	names := args[1]
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep+1 >= len(args) {
		return 0, nil, nil, fmt.Errorf("missing -- cmd")
	}
	argv = args[sep+1:]
	listenEnv = []string{
		"LISTEN_PID=" + strconv.Itoa(pid),
		"LISTEN_FDS=" + strconv.Itoa(n),
		"LISTEN_FDNAMES=" + names,
	}
	return n, argv, listenEnv, nil
}
