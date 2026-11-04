// Command gpioread prints the level of GPIO lines.
//
// Used by the virtual bench (sim/) to watch the brain's GPIO lines from
// inside the guest, and handy on real hardware for checking the two lines
// that decide whether the PiLow keeps the Pi powered:
//
//	GPIO17  nPIRUNNING - driven low by the firmware gpio= directive while
//	        the OS runs (gpio-poweroff keeps it managed for Pi-initiated
//	        power-off). The PiLow reads it through the level shifter and
//	        powers the Pi off if it never goes low.
//	GPIO18  level-shifter output enable - must be high or the PiLow cannot
//	        see nPIRUNNING at all.
//
// It reads the GPIO level registers through /dev/gpiomem rather than claiming
// the lines: GPIO17 is managed by gpio-poweroff and GPIO18 is held by
// pilowhold, and claiming either would steal it (or fail with EBUSY). The
// level registers report what the pins are actually doing.
//
// usage: gpioread <pin> [pin...]
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

const (
	// The BCM2835/2710 GPIO block. /dev/gpiomem maps it read/write.
	gpioBase = 0x7e200000
	// GPLEV0/GPLEV1: one bit per pin, level of GPIO0-31 and GPIO32-63.
	gplev0 = 0x34
	gplev1 = 0x38
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <pin> [pin...]\n", os.Args[0])
		os.Exit(2)
	}

	mem, err := os.OpenFile("/dev/gpiomem", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gpioread: %v\n", err)
		os.Exit(1)
	}
	defer mem.Close()

	// mmap the GPIO block. The kernel's bcm2835-gpiomem driver maps the
	// whole 4KB block at the physical address of the GPIO controller.
	data, err := syscall.Mmap(int(mem.Fd()), 0, 0x1000,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gpioread: mmap: %v\n", err)
		os.Exit(1)
	}
	defer syscall.Munmap(data)

	for _, arg := range os.Args[1:] {
		pin, err := strconv.Atoi(arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gpioread: bad pin %q\n", arg)
			os.Exit(2)
		}
		off := gplev0
		if pin >= 32 {
			off = gplev1
			pin -= 32
		}
		lev := binary.LittleEndian.Uint32(data[off:])
		fmt.Printf("GPIO%d = %d\n", pin, (lev>>uint(pin))&1)
	}
}
