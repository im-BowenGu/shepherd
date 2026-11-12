#!/usr/bin/env bash
# test-camera-ipc.sh - cross-language check of the camera IPC path.
#
# Writes a frame with the *real* robot library's shared-memory writer
# (robot/robot/vision.py), then has the shepherd's Go reader pick it up and
# compare it byte for byte. Catches any drift between the two sides of the
# wire format.
#
# Usage: ./scripts/test-camera-ipc.sh
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
ROBOT_VISION="${ROBOT_VISION:-$ROOT/../robot/robot/vision.py}"
SEG="/shepherd_cam_test"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

if [ ! -d /dev/shm ]; then
  echo "SKIP: /dev/shm not available" >&2
  exit 0
fi

rm -f "/dev/shm/$(basename "$SEG")"

# The writer is the robot library's own code, lifted verbatim so the test
# cannot drift from what the robot actually does.
python3 - "$ROBOT_VISION" "$WORK/frame.bin" <<'PYEOF'
import ctypes, mmap, os, struct, sys

vision_path, out_path = sys.argv[1], sys.argv[2]

# Pull the shared-memory writer out of the robot library and run it, so the
# test exercises the real producer code rather than a copy of it.
source = open(vision_path).read()
start = source.index("_libc = ctypes.CDLL")
end = source.index("from collections.abc import Iterable")
namespace = {"ctypes": ctypes, "mmap": mmap, "os": os, "struct": struct}
exec(compile(source[start:end], vision_path, "exec"), namespace)

namespace["_SHM_NAME"] = b"/shepherd_cam_test"
namespace["_shm_init"]()

payload = b"\xff\xd8\xff\xe0 fake jpeg payload \x00\x01\x02\xfd"
namespace["_shm_write"](payload)
with open(out_path, "wb") as f:
    f.write(payload)
print("wrote %d bytes to /dev/shm/%s" % (len(payload), namespace["_SHM_NAME"].decode()))
PYEOF

# Now read it back with the Go implementation.
cd "$ROOT"
SHEPHERD_TEST_FRAME_FILE="$WORK/frame.bin" \
SHEPHERD_TEST_SHM_NAME="$SEG" \
  go test ./internal/ws/ -run TestShmSourceReadsRobotLibrary -v
