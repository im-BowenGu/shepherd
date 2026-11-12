package ws

import (
	"encoding/base64"
	"encoding/binary"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Shared-memory frame layout (must match robot/robot/vision.py):
//
//	byte 0..8    magic "SHEPCAM "
//	byte 8..16   sequence number (uint64 LE)
//	byte 16..24  payload size (uint64 LE)
//	byte 24..64  padding
//	byte 64..    payload (raw JPEG)
const (
	shmMagic      = "SHEPCAM "
	shmHeaderSize = 64
	shmSize       = 2 * 1024 * 1024
)

// frameSource yields the most recent camera frame.
type frameSource interface {
	// Next waits up to timeout for a new frame. It returns the frame and
	// true, or nil and true if nothing arrived in time. false means the
	// source is closed.
	Next(timeout time.Duration) ([]byte, bool)
	Close() error
}

// --- POSIX shared memory source -------------------------------------------

// shmSource reads frames from a POSIX shared memory region. This is the
// fastest local transport: the producer writes straight into the mapping
// (one memcpy), and the consumer polls the sequence counter - no open(),
// no write(), no read(), no inotify event, no file locking.
type shmSource struct {
	path    string
	data    []byte
	fd      int
	lastSeq uint64
	mu      sync.Mutex
}

func newShmSource(name string) (*shmSource, error) {
	path := name
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = filepath.Join("/dev/shm", filepath.Base(path))

	s := &shmSource{path: path, fd: -1}
	if err := s.open(); err != nil {
		return nil, err
	}
	return s, nil
}

// open maps the segment. The robot creates it when its vision library
// starts, which can be after shepherd, so this is retried.
func (s *shmSource) open() error {
	fd, err := syscall.Open(s.path, syscall.O_RDONLY, 0)
	if err != nil {
		return err
	}
	// The producer creates the segment and then ftruncate()s it to size.
	// Mapping a file that is still short would fault with SIGBUS the
	// moment we read it, so wait until it is fully sized.
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		syscall.Close(fd)
		return err
	}
	if st.Size < int64(shmSize) {
		syscall.Close(fd)
		return syscall.EINVAL
	}
	data, err := syscall.Mmap(fd, 0, shmSize, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		syscall.Close(fd)
		return err
	}
	s.mu.Lock()
	s.data = data
	s.fd = fd
	// Sentinel, not 0: the producer's first frame carries sequence 0, so a
	// reader starting at 0 would never see a change and would sit waiting
	// for a second frame that may never come.
	s.lastSeq = ^uint64(0)
	s.mu.Unlock()
	return nil
}

// Next waits for the sequence number to advance, then returns the payload.
func (s *shmSource) Next(timeout time.Duration) ([]byte, bool) {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		data := s.data
		s.mu.Unlock()

		if data == nil {
			// The segment does not exist yet (or not any more): try to
			// (re)open it, then keep waiting.
			if err := s.open(); err != nil {
				if time.Now().After(deadline) {
					return nil, true
				}
				time.Sleep(100 * time.Millisecond)
				continue
			}
			continue
		}

		if len(data) >= shmHeaderSize && string(data[:8]) == shmMagic {
			seq := binary.LittleEndian.Uint64(data[8:16])
			if seq != s.lastSeq {
				size := binary.LittleEndian.Uint64(data[16:24])
				s.mu.Lock()
				s.lastSeq = seq
				s.mu.Unlock()
				if size == 0 || size > uint64(len(data)-shmHeaderSize) {
					return nil, true
				}
				payload := make([]byte, size)
				copy(payload, data[shmHeaderSize:shmHeaderSize+int(size)])
				return payload, true
			}
		}

		if time.Now().After(deadline) {
			return nil, true
		}
		// 2ms poll: at 10-11fps a frame lands every ~90ms, so this is
		// ~45 wakeups per frame - negligible next to the encode cost.
		time.Sleep(2 * time.Millisecond)
	}
}

func (s *shmSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data != nil {
		syscall.Munmap(s.data)
		s.data = nil
	}
	if s.fd >= 0 {
		syscall.Close(s.fd)
		s.fd = -1
	}
	return nil
}

// --- File source ----------------------------------------------------------

// fileSource is the fallback transport: the producer writes the frame to a
// file on tmpfs and the consumer is woken by an inotify event.
type fileSource struct {
	path string
	ch   chan struct{}
}

func newFileSource(path string) *fileSource {
	return &fileSource{path: path, ch: make(chan struct{}, 1)}
}

func (f *fileSource) Next(timeout time.Duration) ([]byte, bool) {
	select {
	case <-f.ch:
	case <-time.After(timeout):
		return nil, true
	}
	data, err := os.ReadFile(f.path)
	if err != nil || len(data) == 0 {
		return nil, true
	}
	return data, true
}

func (f *fileSource) Close() error { return nil }

// --- UDP source -----------------------------------------------------------

// udpSource receives frames as UDP datagrams. Evaluated as an alternative
// transport: lowest latency (no polling, no syscall per frame) but
// unreliable - a dropped datagram means a dropped frame - and the payload
// has to fit the datagram size.
type udpSource struct {
	conn *os.File
	ch   chan []byte
}

func newUDPSource(addr string) (*udpSource, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		ip = net.IPv4(127, 0, 0, 1)
	}

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, err
	}
	sa := &syscall.SockaddrInet4{Port: port}
	copy(sa.Addr[:], ip.To4())
	if err := syscall.Bind(fd, sa); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "udp-cam")
	u := &udpSource{conn: f, ch: make(chan []byte, 4)}
	go func() {
		buf := make([]byte, 65535)
		for {
			n, err := f.Read(buf)
			if err != nil {
				return
			}
			frame := make([]byte, n)
			copy(frame, buf[:n])
			select {
			case u.ch <- frame:
			default: // drop the oldest slow frame, keep the newest
				select {
				case <-u.ch:
				default:
				}
				select {
				case u.ch <- frame:
				default:
				}
			}
		}
	}()
	return u, nil
}

func (u *udpSource) Next(timeout time.Duration) ([]byte, bool) {
	select {
	case frame, ok := <-u.ch:
		return frame, ok
	case <-time.After(timeout):
		return nil, true
	}
}

func (u *udpSource) Close() error {
	if u.conn != nil {
		u.conn.Close()
	}
	return nil
}

// --- Camera streamer ------------------------------------------------------

type CameraStreamer struct {
	hub        *Hub
	imagePath  string
	mu         sync.Mutex
	currentB64 string
	primary    frameSource
	fallback   frameSource
	stop       chan struct{}
}

func NewCameraStreamer(hub *Hub, imagePath string) *CameraStreamer {
	return &CameraStreamer{
		hub:       hub,
		imagePath: imagePath,
		stop:      make(chan struct{}),
	}
}

// SetPrimary installs the preferred transport to read frames from.
func (c *CameraStreamer) SetPrimary(src frameSource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.primary = src
}

// SetFallback installs the transport used when the primary has no frame.
func (c *CameraStreamer) SetFallback(src frameSource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fallback = src
}

// NewSHMSource opens a POSIX shared memory frame source.
func NewSHMSource(name string) (*shmSource, error) {
	return newShmSource(name)
}

// NewUDPSource opens a UDP frame source.
func NewUDPSource(addr string) (*udpSource, error) {
	return newUDPSource(addr)
}

// NewFileSource opens a file-backed frame source.
func NewFileSource(path string) *fileSource {
	return newFileSource(path)
}

func (c *CameraStreamer) Start() error {
	// Watch the directory, not the file. The vision library writes each frame
	// with os.replace(tmp, imagePath), which swaps in a new inode; a watch on
	// the file itself would follow the original inode and stop firing after the
	// first frame. Watching the directory keeps delivering events across the
	// rename, and we filter on the image's basename.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	dir := filepath.Dir(c.imagePath)
	base := filepath.Base(c.imagePath)
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return err
	}

	fileSrc := newFileSource(c.imagePath)

	// The file transport is always available as a fallback: it works even
	// when /dev/shm is not mounted or the robot is running an older library
	// that does not publish to shared memory.
	c.mu.Lock()
	if c.fallback == nil {
		c.fallback = fileSrc
	}
	c.mu.Unlock()

	go func() {
		defer watcher.Close()

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Base(event.Name) != base {
					continue
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
					select {
					case fileSrc.ch <- struct{}{}:
					default:
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("camera watcher error: %v", err)
			}
		}
	}()

	return nil
}

func (c *CameraStreamer) Close() error {
	close(c.stop)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.primary != nil {
		c.primary.Close()
		c.primary = nil
	}
	if c.fallback != nil {
		c.fallback.Close()
		c.fallback = nil
	}
	return nil
}

// Run pumps frames from the source into the websocket hub.
func (c *CameraStreamer) Run() {
	for {
		select {
		case <-c.stop:
			return
		default:
		}

		c.mu.Lock()
		primary, fallback := c.primary, c.fallback
		c.mu.Unlock()

		var frame []byte
		if primary != nil {
			f, _ := primary.Next(20 * time.Millisecond)
			frame = f
		}
		if len(frame) == 0 && fallback != nil {
			f, _ := fallback.Next(20 * time.Millisecond)
			frame = f
		}
		if len(frame) == 0 {
			continue
		}

		// Nobody watching: skip the work entirely. The cached frame is still
		// served to any client that connects next.
		if !c.hub.HasClients() {
			continue
		}

		// Raw JPEG in, base64 text out: the browser turns it into a data
		// URL. No decode, no resize, no re-encode - the JPEG the robot
		// encoded is the JPEG the browser gets.
		b64 := base64.StdEncoding.EncodeToString(frame)

		c.mu.Lock()
		c.currentB64 = b64
		c.mu.Unlock()

		c.hub.Broadcast([]byte("[CAMERA]" + b64))
	}
}

func (c *CameraStreamer) GetCurrentFrame() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.currentB64
}
