package gpio

// Linux GPIO v2 character-device ABI (kernel >= 5.5), implemented with plain
// syscalls so the static binary needs no libgpiod. The sysfs interface was
// removed in newer kernels and the v1 ioctls are disabled in distro kernels,
// so v2 is the only portable way to touch GPIO on the 6.18 Armbian kernel.
//
// Layouts mirror include/uapi/linux/gpio.h exactly. Two details matter and
// both were wrong before: GPIO_MAX_NAME_SIZE is 32 (not 128) and the offsets
// array is __u32[64] (not __u64[64]), so a struct built with the wrong field
// widths produces a different size, the ioctl number encodes that size and
// the kernel rejects every request with ENOTTY. sizeof(struct
// gpio_v2_line_request) is 592 on 64-bit.

import (
	"fmt"
	"log"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

const (
	// gpio_v2_line_flag (enum from the kernel header)
	flagInput      = 1 << 2
	flagOutput     = 1 << 3
	flagBiasPullUp = 1 << 8

	// gpio_v2_line_attr_id
	attrIDOutputValues = 2

	// ioctls
	ioctlGetLine       = 0x07
	ioctlLineGetValues = 0x0e
	ioctlLineSetValues = 0x0f
)

var chipPaths = []string{
	"/dev/gpiochip0",
	"/dev/gpiochip1",
	"/dev/gpiochip2",
	"/dev/gpiochip3",
}

type lineAttribute struct {
	ID      uint32
	Padding uint32
	Value   uint64
}

type lineConfigAttribute struct {
	Attr lineAttribute
	Mask uint64
}

type lineConfig struct {
	Flags    uint64
	NumAttrs uint32
	Padding  [5]uint32
	Attrs    [10]lineConfigAttribute
}

type lineRequest struct {
	Offsets         [64]uint32
	Consumer        [32]byte
	Config          lineConfig
	NumLines        uint32
	EventBufferSize uint32
	Padding         [5]uint32
	FD              int32
}

type lineValues struct {
	Bits uint64
	Mask uint64
}

// ioctlNum encodes _IOWR(0xB4, nr, sizeof(struct)) - the kernel matches on
// this size, so it must track the Go struct exactly.
func ioctlNum(nr uintptr, size uintptr) uintptr {
	return (3 << 30) | (size << 16) | (0xb4 << 8) | nr
}

func getLineIoctl() uintptr {
	return ioctlNum(ioctlGetLine, unsafe.Sizeof(lineRequest{}))
}

func valuesIoctl(nr uintptr) uintptr {
	return ioctlNum(nr, unsafe.Sizeof(lineValues{}))
}

// Line is an open GPIO line handle.
type Line struct {
	FD   int
	Chip string
	Pin  int
	// Biased is true when the line was claimed with the internal pull-up
	// enabled. A line that was claimed without it floats, so its level says
	// nothing about whatever is (or is not) driving it.
	Biased bool
}

// Close releases the line so the kernel drops our claim on it.
func (l *Line) Close() {
	if l != nil && l.FD >= 0 {
		syscall.Close(l.FD)
		l.FD = -1
	}
}

// Read returns the current level (0 or 1).
func (l *Line) Read() (int, error) {
	var v lineValues
	v.Mask = 1
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(l.FD),
		valuesIoctl(ioctlLineGetValues), uintptr(unsafe.Pointer(&v)))
	if errno != 0 {
		return 0, errno
	}
	if v.Bits&1 != 0 {
		return 1, nil
	}
	return 0, nil
}

// Write drives an output line.
func (l *Line) Write(value int) error {
	var v lineValues
	v.Bits = uint64(value)
	v.Mask = 1
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(l.FD),
		valuesIoctl(ioctlLineSetValues), uintptr(unsafe.Pointer(&v)))
	if errno != 0 {
		return errno
	}
	return nil
}

// RequestOutput claims pin on the first gpiochip that has it as an output,
// optionally with an initial value. The returned Line must be kept open (or
// closed) deliberately: the kernel releases the claim when the fd closes,
// which matters for pins that gate other hardware.
func RequestOutput(pin int, consumer string, initial int) (*Line, error) {
	req := lineRequest{
		NumLines: 1,
		Config: lineConfig{
			Flags:    flagOutput,
			NumAttrs: 1,
			Attrs: [10]lineConfigAttribute{
				0: {
					Attr: lineAttribute{ID: attrIDOutputValues, Value: uint64(initial)},
					Mask: 1,
				},
			},
		},
	}
	req.Offsets[0] = uint32(pin)
	copy(req.Consumer[:], consumer)
	return doRequest(pin, &req)
}

// RequestInput claims pin as an input with the internal pull-up enabled. This
// is how the PiLow start button line is read. Some pads reject an explicit
// bias request (the pull already matches what the hardware wants), so the
// claim is retried with the bias unspecified - getting the line is what
// matters, and the caller polls it either way.
func RequestInput(pin int, consumer string) (*Line, error) {
	line, err := requestInput(pin, consumer, flagInput|flagBiasPullUp)
	if err == nil {
		line.Biased = true
		return line, nil
	}
	log.Printf("GPIO: line %d with pull-up rejected (%v), retrying without bias", pin, err)
	return requestInput(pin, consumer, flagInput)
}

func requestInput(pin int, consumer string, flags uint64) (*Line, error) {
	req := lineRequest{
		NumLines: 1,
		Config:   lineConfig{Flags: flags},
	}
	req.Offsets[0] = uint32(pin)
	copy(req.Consumer[:], consumer)
	return doRequest(pin, &req)
}

func doRequest(pin int, req *lineRequest) (*Line, error) {
	var errs []string
	busy := false
	for _, chip := range chipPaths {
		chipFd, err := syscall.Open(chip, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: open %v", chip, err))
			continue
		}
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(chipFd),
			getLineIoctl(), uintptr(unsafe.Pointer(req)))
		syscall.Close(chipFd)
		if errno == 0 {
			log.Printf("GPIO: claimed line %d on %s (fd %d)", pin, chip, req.FD)
			return &Line{FD: int(req.FD), Chip: chip, Pin: pin}, nil
		}
		if errno == syscall.EBUSY {
			busy = true
		}
		errs = append(errs, fmt.Sprintf("%s: %v", chip, errno))
	}
	// The errno matters: EBUSY means something else (often shepherd itself)
	// already holds the line, EINVAL means the request flags were rejected.
	// Wrap EBUSY so callers can tell "someone else owns it" (harmless) from
	// "this line cannot be requested" with errors.Is.
	if busy {
		return nil, fmt.Errorf("cannot claim GPIO line %d: %s: %w", pin, strings.Join(errs, "; "), syscall.EBUSY)
	}
	return nil, fmt.Errorf("cannot claim GPIO line %d: %s", pin, strings.Join(errs, "; "))
}

// ChipPaths lists the GPIO character devices present, for diagnostics.
func ChipPaths() []string {
	var out []string
	for _, chip := range chipPaths {
		if _, err := os.Stat(chip); err == nil {
			out = append(out, chip)
		}
	}
	return out
}
