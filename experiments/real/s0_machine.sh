#!/bin/sh
# S0: the machine card. Everything a reader needs to judge the numbers that follow.
#
# It exists because the chain budget is measured in seconds, and seconds depend on the box: a laptop
# GPU that throttles, a vhdx that reports itself as rotational, a WSL memory limit half the host's.
# Without this file the results are anecdotes.
#
#   sh experiments/real/s0_machine.sh
#
# Writes experiments/real/results/s0-machine.json and prints the same lines.
set -eu

HERE=$(cd "$(dirname "$0")/../.." && pwd)
OUT="$HERE/experiments/real/results"
mkdir -p "$OUT"

PYTHON=${PYTHON:-python3}

echo "== machine card =="
nvidia-smi --query-gpu=name,driver_version,memory.total,temperature.gpu,clocks.sm,power.draw \
	--format=csv,noheader 2>/dev/null || echo "no nvidia-smi"
grep -m1 "model name" /proc/cpuinfo | cut -d: -f2 | sed 's/^ /cpu: /'
printf 'vcpu: %s\n' "$(nproc)"
printf 'mem total: %s\n' "$(awk '/MemTotal/ {printf "%.1f GiB", $2/1048576}' /proc/meminfo)"
printf 'checkpoint volume: %s\n' "$(df -h /root | tail -1)"

echo "== disk: sequential write, 256 MiB, fsync'd =="
dd if=/dev/zero of=/root/tmp/s0-write bs=1M count=256 conv=fdatasync 2>&1 | tail -1
rm -f /root/tmp/s0-write

echo "== disk: fsync latency, 20 small files =="
"$PYTHON" - <<'PY'
import os, time
d = "/root/tmp/s0-fsync"
os.makedirs(d, exist_ok=True)
t = []
for i in range(20):
    p = os.path.join(d, f"f{i}.bin")
    s = time.perf_counter()
    with open(p, "wb") as f:
        f.write(b"x" * 65536)
        f.flush()
        os.fsync(f.fileno())
    t.append(time.perf_counter() - s)
t.sort()
print("median fsync: %.2f ms  p95: %.2f ms" % (t[10] * 1000, t[18] * 1000))
PY

"$PYTHON" - "$OUT/s0-machine.json" <<'PY'
import json, os, platform, subprocess, sys, time

def run(cmd):
    try:
        return subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=30).stdout.strip()
    except Exception as e:  # noqa: BLE001 - a missing tool is part of the card
        return f"unavailable: {type(e).__name__}"

card = {
    "at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
    "kernel": platform.release(),
    "python": platform.python_version(),
    "nvidia_smi": run("nvidia-smi --query-gpu=name,driver_version,memory.total --format=csv,noheader"),
    "cpu": run("grep -m1 'model name' /proc/cpuinfo | cut -d: -f2").strip(),
    "vcpu": os.cpu_count(),
    "mem_total_kib": int(run("awk '/MemTotal/ {print $2}' /proc/meminfo") or 0),
    "disk_free_bytes": os.statvfs("/root").f_bavail * os.statvfs("/root").f_frsize,
    "disk_write_rate": run(
        "dd if=/dev/zero of=/root/tmp/s0-bs bs=1M count=256 conv=fdatasync 2>&1 "
        "| awk '/copied/ {print $NF}'; rm -f /root/tmp/s0-bs"
    ),
    "torch": run(f"{sys.executable} -c 'import torch; print(torch.__version__)'"),
    "cuda": run(f"{sys.executable} -c 'import torch; print(torch.version.cuda)'"),
    "arch": run(f"{sys.executable} -c 'import torch; print(torch.cuda.get_device_name(0))'"),
}
with open(sys.argv[1], "w") as f:
    json.dump(card, f, indent=1)
print("written:", sys.argv[1])
PY
