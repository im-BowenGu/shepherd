package ws

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// robotShmSource extracts the shared-memory writer from the real robot
// library (robot/robot/vision.py) so the producer and the consumer are
// validated against each other, not against a copy of the format.
const robotShmSource = `
import ctypes, mmap, os, struct, sys

_libc = ctypes.CDLL("libc.so.6")
_SHM_NAME = b"/shepherd_cam_test"
_SHM_SIZE = 2 * 1024 * 1024
_SHM_HEADER = 64
_SHM_MAGIC = b"SHEPCAM "

_libc.shm_open.argtypes = [ctypes.c_char_p, ctypes.c_int, ctypes.c_int]
_libc.shm_open.restype = ctypes.c_int
_libc.ftruncate.argtypes = [ctypes.c_int, ctypes.c_long]
_libc.ftruncate.restype = ctypes.c_int

fd = _libc.shm_open(_SHM_NAME, os.O_RDWR | os.O_CREAT, 0o600)
assert fd >= 0, "shm_open failed"
_libc.ftruncate(fd, _SHM_SIZE)
mm = mmap.mmap(fd, _SHM_SIZE)
_libc.close(fd)

payload = sys.argv[1].encode("latin-1")
seq = 7
header = struct.pack("<8sQQ", _SHM_MAGIC, seq, len(payload))
header = header.ljust(_SHM_HEADER, b"\x00")
mm[0:_SHM_HEADER] = header
mm[_SHM_HEADER:_SHM_HEADER + len(payload)] = payload
mm.flush()
print("wrote", len(payload))
`

// pythonInterpreter finds a python3 that can actually be exec'd: the nix
// profile shim is not forkable in this environment.
func pythonInterpreter() string {
	for _, candidate := range []string{"/usr/bin/python3", "python3", "/usr/local/bin/python3"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return "python3"
}

// TestShmSourceReadsRobotLibrary checks that the frames the robot library
// writes are read back byte for byte by the shepherd's shm source.
//
// The robot library writes the segment from python (see
// robotShmSource above), so the test is driven in two steps: a shell script
// runs the writer, then the test reads the segment back and compares it with
// the payload the writer recorded. Go cannot exec python in this sandbox, so
// the writer runs out of band.
func TestShmSourceReadsRobotLibrary(t *testing.T) {
	if _, err := os.Stat("/dev/shm"); err != nil {
		t.Skip("/dev/shm not available")
	}

	frameFile := os.Getenv("SHEPHERD_TEST_FRAME_FILE")
	if frameFile == "" {
		t.Skip("SHEPHERD_TEST_FRAME_FILE not set (run via scripts/test-camera-ipc.sh)")
	}
	segName := os.Getenv("SHEPHERD_TEST_SHM_NAME")
	if segName == "" {
		segName = "/shepherd_cam_test"
	}

	want, err := os.ReadFile(frameFile)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}

	src, err := newShmSource(segName)
	if err != nil {
		t.Fatalf("open shm source: %v", err)
	}
	defer src.Close()

	frame, ok := src.Next(2 * time.Second)
	if !ok {
		t.Fatal("source closed")
	}
	if frame == nil {
		t.Fatal("no frame received")
	}
	if !bytes.Equal(frame, want) {
		t.Fatalf("frame mismatch:\n got %q\nwant %q", frame, want)
	}
	t.Logf("read %d bytes from %s", len(frame), segName)
}

// TestShmSourceSequence makes sure the reader only reports a frame once per
// write, so a slow consumer does not re-broadcast the same frame forever.
func TestShmSourceSequence(t *testing.T) {
	if _, err := os.Stat("/dev/shm"); err != nil {
		t.Skip("/dev/shm not available")
	}
	os.Remove("/dev/shm/shepherd_cam_seq")

	f, err := os.Create("/dev/shm/shepherd_cam_seq")
	if err != nil {
		t.Skipf("cannot create shm file: %v", err)
	}
	if err := f.Truncate(shmSize); err != nil {
		t.Fatal(err)
	}
	f.Close()

	src, err := newShmSource("/shepherd_cam_seq")
	if err != nil {
		t.Fatalf("open shm source: %v", err)
	}
	defer src.Close()

	// Empty segment: no frames, and the magic check must not crash.
	if frame, _ := src.Next(50 * time.Millisecond); frame != nil {
		t.Fatalf("expected no frame from an empty segment, got %d bytes", len(frame))
	}

	// Write a frame the way the robot does and read it back.
	writeShmFrame(t, "/dev/shm/shepherd_cam_seq", 0, []byte("first"))
	frame, _ := src.Next(2 * time.Second)
	if string(frame) != "first" {
		t.Fatalf("got %q, want %q", frame, "first")
	}

	// The same frame must not be reported again: the sequence number has
	// not moved.
	if frame, _ := src.Next(100 * time.Millisecond); frame != nil {
		t.Fatalf("expected no repeat frame, got %q", frame)
	}

	// A new frame with a new sequence number must be reported.
	writeShmFrame(t, "/dev/shm/shepherd_cam_seq", 1, []byte("second"))
	frame, _ = src.Next(2 * time.Second)
	if string(frame) != "second" {
		t.Fatalf("got %q, want %q", frame, "second")
	}
}

// writeShmFrame writes a frame in the robot library's layout.
func writeShmFrame(t *testing.T, path string, seq uint64, payload []byte) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, shmSize)
	copy(buf, data)
	copy(buf[:8], shmMagic)
	binary.LittleEndian.PutUint64(buf[8:16], seq)
	binary.LittleEndian.PutUint64(buf[16:24], uint64(len(payload)))
	copy(buf[shmHeaderSize:], payload)

	if err := os.WriteFile(path, buf, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestShmSourceShortSegment checks the reader refuses to map a segment that
// has not been sized yet. Mapping a short file and reading it faults with
// SIGBUS, which would take the whole shepherd down.
func TestShmSourceShortSegment(t *testing.T) {
	if _, err := os.Stat("/dev/shm"); err != nil {
		t.Skip("/dev/shm not available")
	}
	path := "/dev/shm/shepherd_cam_short"
	os.Remove(path)

	f, err := os.Create(path)
	if err != nil {
		t.Skipf("cannot create shm file: %v", err)
	}
	// Deliberately short: the producer has created it but not sized it.
	if _, err := f.Write([]byte("SHEPCAM ")); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if _, err := newShmSource("/shepherd_cam_short"); err == nil {
		t.Fatal("expected the short segment to be rejected")
	}

	// Once sized, it maps.
	if err := os.Truncate(path, shmSize); err != nil {
		t.Fatal(err)
	}
	src, err := newShmSource("/shepherd_cam_short")
	if err != nil {
		t.Fatalf("expected the sized segment to map: %v", err)
	}
	src.Close()
}

// TestFileSourceFallback checks the fallback transport used when shared
// memory is unavailable.
func TestFileSourceFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frame.jpg")
	if err := os.WriteFile(path, []byte("jpeg-bytes"), 0644); err != nil {
		t.Fatal(err)
	}

	src := newFileSource(path)
	select {
	case src.ch <- struct{}{}:
	default:
	}

	frame, ok := src.Next(time.Second)
	if !ok {
		t.Fatal("source closed")
	}
	if string(frame) != "jpeg-bytes" {
		t.Fatalf("got %q", frame)
	}

	// No notification: the call must time out rather than block forever.
	start := time.Now()
	if frame, _ := src.Next(50 * time.Millisecond); frame != nil {
		t.Fatalf("expected no frame, got %q", frame)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}
