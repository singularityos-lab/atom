package pid1

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	pressureFile    = "/proc/pressure/memory"
	pressureTrigger = "some 100000 1000000"
	watchInterval   = 100 * time.Millisecond
	calmPolls       = 50
	pfKthread       = 0x00200000
	sysPidfdOpen    = 434
	sysPidfdSignal  = 424
	sysMrelease     = 448
	mclCurrent      = 1
	mclFuture       = 2
	mclOnfault      = 4
)

var procRoot = "/proc"

type memInfo struct {
	total, available, swapTotal, swapFree uint64
}

func (m memInfo) floor() uint64 {
	f := m.total / 32
	if f < 256<<20 {
		f = 256 << 20
	}
	return f
}

func (m memInfo) exhausted(stalled float64) bool {
	if m.total == 0 || m.available > m.floor() {
		return false
	}
	return m.swapTotal == 0 || m.swapFree < m.swapTotal/4 || stalled >= 0.10
}

type proc struct {
	pid    int
	sid    int
	cgroup string
	comm   string
	anon   uint64
}

type victim struct {
	cgroup string
	sid    int
	anon   uint64
	pids   []int
	leader string
}

func startMemoryGuard() {
	if cmdlineValue("sinit.memguard") == "0" {
		logf("memory guard disabled (sinit.memguard=0)")
		return
	}
	f, err := os.OpenFile(pressureFile, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		logf("memory guard: %v", err)
		return
	}
	if _, err := f.Write([]byte(pressureTrigger + "\x00")); err != nil {
		f.Close()
		logf("memory guard: trigger: %v", err)
		return
	}
	if err := lockResident(); err != nil {
		logf("memory guard: mlockall: %v", err)
	}
	logf("memory guard armed on %s", pressureFile)
	go runMemoryGuard(f)
}

func lockResident() error {
	if _, _, errno := syscall.Syscall(syscall.SYS_MLOCKALL, mclCurrent|mclFuture|mclOnfault, 0, 0); errno != 0 {
		return errno
	}
	return nil
}

func runMemoryGuard(f *os.File) {
	fds := []pollFd{{fd: int32(f.Fd()), events: pollPri}}
	for {
		n, err := poll(fds)
		if err != nil && err != syscall.EINTR {
			logf("memory guard: poll: %v", err)
			return
		}
		if n <= 0 || fds[0].revents&pollErr != 0 {
			continue
		}
		watch()
	}
}

func watch() {
	meminfo := filepath.Join(procRoot, "meminfo")
	lastStall, _ := readFullStall(pressureFile)
	last := time.Now()
	calm := 0
	stuck := false
	for calm < calmPolls {
		time.Sleep(watchInterval)
		m, err := readMemInfo(meminfo)
		if err != nil {
			return
		}
		stall, _ := readFullStall(pressureFile)
		now := time.Now()
		frac := float64(stall-lastStall) / float64(now.Sub(last).Microseconds())
		lastStall, last = stall, now
		if m.available > 2*m.floor() {
			calm++
			continue
		}
		calm = 0
		if m.exhausted(frac) && !relieve(m, frac, stuck) {
			stuck = true
		}
	}
}

func relieve(m memInfo, stalled float64, quiet bool) bool {
	v, ok := pickVictim(scanProcs())
	if !ok {
		if !quiet {
			logf("memory guard: memory exhausted and nothing left to stop")
		}
		return false
	}
	killed := 0
	for _, pid := range v.pids {
		if kill(pid) {
			killed++
		}
	}
	logf("memory guard: stopped %s (session %d, %d processes, %d MB) in %s; available %d MB, swap free %d MB, stalled %d%%",
		v.leader, v.sid, killed, v.anon>>20, v.cgroup, m.available>>20, m.swapFree>>20, int(stalled*100))
	return true
}

func readFullStall(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "full ") {
			continue
		}
		for _, f := range strings.Fields(line) {
			if v, ok := strings.CutPrefix(f, "total="); ok {
				return strconv.ParseUint(v, 10, 64)
			}
		}
	}
	return 0, nil
}

func readMemInfo(path string) (memInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return memInfo{}, err
	}
	var m memInfo
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		v <<= 10
		switch f[0] {
		case "MemTotal:":
			m.total = v
		case "MemAvailable:":
			m.available = v
		case "SwapTotal:":
			m.swapTotal = v
		case "SwapFree:":
			m.swapFree = v
		}
	}
	return m, nil
}

func scanProcs() []proc {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	self := os.Getpid()
	var out []proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == 1 || pid == self {
			continue
		}
		p, ok := readProc(pid)
		if ok {
			out = append(out, p)
		}
	}
	return out
}

func readProc(pid int) (proc, bool) {
	dir := filepath.Join(procRoot, strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return proc{}, false
	}
	end := bytes.LastIndexByte(stat, ')')
	start := bytes.IndexByte(stat, '(')
	if start < 0 || end < start {
		return proc{}, false
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 7 {
		return proc{}, false
	}
	flags, _ := strconv.ParseUint(fields[6], 10, 64)
	if flags&pfKthread != 0 || fields[0] == "Z" || fields[0] == "X" {
		return proc{}, false
	}
	if adj, err := os.ReadFile(filepath.Join(dir, "oom_score_adj")); err == nil && strings.TrimSpace(string(adj)) == "-1000" {
		return proc{}, false
	}
	sid, _ := strconv.Atoi(fields[3])
	p := proc{pid: pid, sid: sid, comm: string(stat[start+1 : end])}
	status, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return proc{}, false
	}
	sc := bufio.NewScanner(bytes.NewReader(status))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || (f[0] != "RssAnon:" && f[0] != "VmSwap:") {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		p.anon += v << 10
	}
	if cg, err := os.ReadFile(filepath.Join(dir, "cgroup")); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(cg)), "\n") {
			if rest, ok := strings.CutPrefix(line, "0::"); ok {
				p.cgroup = rest
			}
		}
	}
	return p, p.anon > 0
}

func protectedCgroup(cg string) bool {
	if cg == "" || cg == "/" || cg == "/init.scope" || strings.HasPrefix(cg, "/system.slice/") {
		return true
	}
	return strings.HasPrefix(filepath.Base(cg), "session-") && strings.HasSuffix(cg, ".scope")
}

func pickVictim(procs []proc) (victim, bool) {
	type key struct {
		cgroup string
		sid    int
	}
	groups := map[key]*victim{}
	for _, p := range procs {
		sid := p.sid
		if sid <= 1 {
			sid = p.pid
		}
		k := key{p.cgroup, sid}
		v := groups[k]
		if v == nil {
			v = &victim{cgroup: p.cgroup, sid: sid}
			groups[k] = v
		}
		v.anon += p.anon
		v.pids = append(v.pids, p.pid)
		if p.pid == sid || v.leader == "" {
			v.leader = p.comm
		}
	}
	var open, guarded []*victim
	for _, v := range groups {
		if protectedCgroup(v.cgroup) {
			guarded = append(guarded, v)
		} else {
			open = append(open, v)
		}
	}
	for _, set := range [][]*victim{open, guarded} {
		if len(set) == 0 {
			continue
		}
		sort.Slice(set, func(i, j int) bool {
			if set[i].anon != set[j].anon {
				return set[i].anon > set[j].anon
			}
			return set[i].sid < set[j].sid
		})
		v := *set[0]
		sort.Ints(v.pids)
		return v, true
	}
	return victim{}, false
}

func kill(pid int) bool {
	fd, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(pid), 0, 0)
	if errno != 0 {
		return syscall.Kill(pid, syscall.SIGKILL) == nil
	}
	defer syscall.Close(int(fd))
	if _, _, errno := syscall.Syscall6(sysPidfdSignal, fd, uintptr(syscall.SIGKILL), 0, 0, 0, 0); errno != 0 {
		return false
	}
	_, _, _ = syscall.Syscall(sysMrelease, fd, 0, 0)
	return true
}

const (
	pollPri = 0x2
	pollErr = 0x8
)

type pollFd struct {
	fd      int32
	events  int16
	revents int16
}

func poll(fds []pollFd) (int, error) {
	n, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&fds[0])), uintptr(len(fds)), 0, 0, 0, 0)
	if errno != 0 {
		return int(n), errno
	}
	return int(n), nil
}
