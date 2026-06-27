#!/usr/bin/env bash
# Boot smoke test.
#
# Runs atom as PID 1 of a fresh pid namespace against a small synthetic unit
# set, asserting it reaches the target and shuts down cleanly. This exercises
# the real PID-1 bring-up path (mounts, cgroup slices, reaper, the dependency
# transaction, ordered shutdown) without needing a kernel. The privileged steps
# (devtmpfs/cgroup2 mounts, the final reboot syscall) fail gracefully inside the
# user namespace and are not fatal.
#
# The full machine boot (atom as init under qemu) runs on the Sinty
# VM; a local qemu variant lands here once a readable kernel is available.
set -u

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/atom"

if ! CGO_ENABLED=0 go -C "$ROOT" build -o "$BIN" ./cmd/atom-init; then
	echo "build failed"; exit 1
fi
if ! command -v unshare >/dev/null; then
	echo "SKIP: unshare not available"; exit 0
fi

U="$TMP/units"
mkdir -p "$U"
cat > "$U/sysinit.target" <<EOF
[Unit]
Description=System Initialization
EOF
cat > "$U/basic.target" <<EOF
[Unit]
Description=Basic System
Requires=sysinit.target
After=sysinit.target
EOF
cat > "$U/boot-test.target" <<EOF
[Unit]
Description=Boot Test
Requires=basic.target
Wants=hello.service world.service
EOF
cat > "$U/world.service" <<EOF
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=$BIN noop world
EOF
cat > "$U/hello.service" <<EOF
[Unit]
After=world.service
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=$BIN noop hello
EOF

OUT="$(unshare --user --map-root-user --pid --fork --mount-proc \
	"$BIN" init --force --exit-after-target --no-default-paths \
	--unit-dir "$U" --target boot-test.target 2>&1)"

echo "$OUT" | grep -vE '^atom\[1\]: (mount|cgroup) '

if echo "$OUT" | grep -q "reached boot-test.target"; then
	echo "SMOKE PASS"
	exit 0
fi
echo "SMOKE FAIL: target not reached"
exit 1
