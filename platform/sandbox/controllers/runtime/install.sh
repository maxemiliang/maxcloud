#!/bin/sh
# Installs gVisor (runsc) on the node this pod runs on and registers it with
# containerd as the runtime handler `runsc`. Runs in the privileged
# gvisor-installer DaemonSet (installer.yaml): the node's / is at /host, and
# hostPID lets nsenter reach the node's systemd.
#
# UKS has no hook for node setup (no user data, no custom image), so this is
# the only way to get a runtime onto a node. It is not something UpCloud
# supports. The loop at the end re-checks every few minutes, because we don't
# know yet whether UKS ever rewrites containerd's config.
#
# Safe to re-run: binaries are skipped when the release is already there, the
# config is only appended to (a copy is kept next to it), and a containerd
# that doesn't come back with the new config gets the old one back.
set -eu

: "${GVISOR_RELEASE:?the release to install, e.g. 20261102}"
: "${GVISOR_SHA512:?sha512 of that release's x86_64 gvisor.tar.bz2}"

HOST=/host
BIN=/usr/local/bin
CONFIG=${CONTAINERD_CONFIG:-/etc/containerd/config.toml}
RECHECK_SECONDS=${RECHECK_SECONDS:-300}

log() { echo "$(date -u +%FT%TZ) $*"; }

# Run a command in the node's namespaces (PID 1 is the node's init).
host() { nsenter -t 1 -m -u -i -n -p -- "$@"; }

# What the spike needs to know about a UKS node, printed once at start.
facts() {
  log "fact os: $(. "$HOST/etc/os-release" && echo "$PRETTY_NAME")"
  log "fact kernel: $(uname -r)"
  log "fact containerd: $(host containerd --version 2>&1 || echo unknown)"
  log "fact containerd config: $CONFIG, $(grep -E '^version' "$HOST$CONFIG" 2>/dev/null || echo 'no version line')"
  log "fact containerd imports: $(grep -E '^imports' "$HOST$CONFIG" 2>/dev/null || echo none)"
  log "fact cgroups: $([ -f "$HOST/sys/fs/cgroup/cgroup.controllers" ] && echo v2 || echo v1)"
  # Kata Containers needs both: the CPU flag passed through, and /dev/kvm.
  log "fact cpu virtualisation flags (vmx/svm): $(grep '^flags' /proc/cpuinfo | grep -c -w -E 'vmx|svm' || true) of $(grep -c ^processor /proc/cpuinfo) CPUs"
  log "fact /dev/kvm: $([ -e "$HOST/dev/kvm" ] && echo present || echo absent)"
}

install_binaries() {
  if [ "$(cat "$HOST$BIN/.gvisor-release" 2>/dev/null || true)" = "$GVISOR_RELEASE" ] &&
    [ -x "$HOST$BIN/runsc" ] && [ -x "$HOST$BIN/containerd-shim-runsc-v1" ]; then
    return 0
  fi
  tmp=$(mktemp -d)
  # Each step is checked by hand: `set -e` is off inside a function that an
  # `if` calls, and a failed download or checksum must never reach tar.
  #
  # x86_64 only: the checksum is that tarball's. bz2 because busybox tar reads
  # it; the .zstd one needs a zstd binary.
  if ! wget -q -O "$tmp/gvisor.tar.bz2" \
    "https://storage.googleapis.com/gvisor/releases/release/$GVISOR_RELEASE/x86_64/gvisor.tar.bz2"; then
    log "error: downloading gVisor $GVISOR_RELEASE failed"
    rm -rf "$tmp"
    return 1
  fi
  if ! echo "$GVISOR_SHA512  $tmp/gvisor.tar.bz2" | sha512sum -c - >/dev/null; then
    log "error: gVisor $GVISOR_RELEASE doesn't match GVISOR_SHA512; nothing installed"
    rm -rf "$tmp"
    return 1
  fi
  # runsc, containerd-shim-runsc-v1 and gvisor-bin/ (helpers runsc looks for
  # next to itself).
  if ! { mkdir -p "$HOST$BIN" && tar -xjf "$tmp/gvisor.tar.bz2" -C "$HOST$BIN" &&
    echo "$GVISOR_RELEASE" >"$HOST$BIN/.gvisor-release"; }; then
    log "error: unpacking gVisor into $BIN failed"
    rm -rf "$tmp"
    return 1
  fi
  rm -rf "$tmp"
  log "installed gVisor $GVISOR_RELEASE in $BIN: $(host "$BIN/runsc" --version 2>&1 | head -n1)"
}

# True when containerd's merged configuration (imports included) has runsc.
registered() { host containerd config dump 2>/dev/null | grep -q 'runtimes\.runsc\]'; }

containerd_up() {
  i=0
  while [ "$i" -lt 30 ]; do
    if host systemctl is-active --quiet containerd && registered; then return 0; fi
    i=$((i + 1))
    sleep 2
  done
  return 1
}

register_runtime() {
  cfg=$HOST$CONFIG
  if registered; then return 0; fi
  [ -f "$cfg" ] || { log "error: no $CONFIG on the node; set CONTAINERD_CONFIG"; return 1; }

  # The CRI plugin's name changed between config version 2 (containerd 1.x,
  # and what a 2.x still reads) and 3; version 4 (containerd 2.3) kept 3's.
  # Anything else we don't touch.
  if grep -qE '^version *= *[34]' "$cfg"; then
    table="plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runsc"
  elif grep -qE '^version *= *2' "$cfg"; then
    table='plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runsc'
  else
    log "error: $CONFIG isn't version 2, 3 or 4; not touching it"
    return 1
  fi

  cp -p "$cfg" "$cfg.pre-gvisor" || return 1
  printf '\n# Added by the gvisor-installer DaemonSet (sandbox-system).\n[%s]\n  runtime_type = "io.containerd.runsc.v1"\n' "$table" >>"$cfg"

  # Running containers keep running through a containerd restart (their shims
  # hold them), this pod included.
  host systemctl restart containerd
  if containerd_up; then
    log "registered runsc in $CONFIG ($table) and restarted containerd"
    return 0
  fi

  log "error: containerd didn't come back with runsc registered; restoring $CONFIG"
  cp -p "$cfg.pre-gvisor" "$cfg"
  host systemctl restart containerd
  return 1
}

facts
while :; do
  # A second "registered runsc" line in this log means something on the node
  # took the entry out again.
  if install_binaries && register_runtime; then
    touch /tmp/ready
  else
    rm -f /tmp/ready
  fi
  sleep "$RECHECK_SECONDS"
done
