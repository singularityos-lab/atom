#!/usr/bin/env bash
# Timer boot test.
#
# Boots atom as PID 1 of a fresh pid namespace with an enabled .timer (OnBootSec=1s)
# and asserts the scheduler fires it: the timer's service touches a marker file. The
# service is NOT in any target's .wants, so the marker can only appear if the timer
# scheduler activated it, which is the property under test. Same user-namespace path
# as smoke.sh; privileged steps fail gracefully and are not fatal.
set -u

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/atom"

if ! CGO_ENABLED=0 go -C "$ROOT" build -o "$BIN" ./cmd/sinit; then
	echo "build failed"; exit 1
fi
if ! command -v unshare >/dev/null; then
	echo "SKIP: unshare not available"; exit 0
fi

U="$TMP/units"; mkdir -p "$U/boot-test.target.wants"
printf '[Unit]\nDescription=System Initialization\n' > "$U/sysinit.target"
printf '[Unit]\nRequires=sysinit.target\nAfter=sysinit.target\n' > "$U/basic.target"
printf '[Unit]\nRequires=basic.target\n' > "$U/boot-test.target"
printf '[Timer]\nOnBootSec=1s\n' > "$U/marker.timer"
printf '[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/usr/bin/touch %s/fired\n' "$TMP" > "$U/marker.service"
# Enable only the timer; the service is reached solely by the timer firing.
ln -s ../marker.timer "$U/boot-test.target.wants/marker.timer"

# Boot WITHOUT --exit-after-target so the timer scheduler runs, then signal shutdown.
unshare --user --map-root-user --pid --fork --mount-proc \
	"$BIN" init --force --no-default-paths --unit-dir "$U" --target boot-test.target \
	>"$TMP/out.log" 2>&1 &
SINIT=$!

rc=1
for _ in $(seq 1 20); do
	if [ -f "$TMP/fired" ]; then rc=0; break; fi
	sleep 0.5
done
kill -TERM "$SINIT" 2>/dev/null

if [ "$rc" = 0 ]; then
	echo "TIMER PASS"
else
	echo "TIMER FAIL: enabled .timer did not fire at boot"
	grep -vE '^atom\[1\]: (mount|cgroup) ' "$TMP/out.log" | tail -20
fi
exit "$rc"
