package gpio

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Button polls a GPIO line (rising to falling edge) for a start button press.
// The pin is the SoC line offset on the first gpiochip that has the line
// (e.g. 26 for BCM GPIO 26 on the Raspberry Pi), which is the PiLow start
// button.
type Button struct {
	pin      int
	arenaUSB string
	onStart  StartHandler

	mu sync.Mutex
	fd int // open line handle (>=0 when poll() is running)
}

// StartHandler is called when the start button is pressed.
type StartHandler func(zone, arena string)

func NewButton(pin int, arenaUSBPath string, handler StartHandler) *Button {
	return &Button{
		pin:      pin,
		arenaUSB: arenaUSBPath,
		onStart:  handler,
		fd:       -1,
	}
}

func (b *Button) Start() {
	go b.poll()
}

func (b *Button) poll() {
	line, err := RequestInput(b.pin, "shepherd-start")
	if err != nil {
		log.Printf("GPIO start button on pin %d unavailable: %v", b.pin, err)
		return
	}
	defer line.Close()

	b.mu.Lock()
	b.fd = line.FD
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.fd = -1
		b.mu.Unlock()
	}()

	log.Printf("GPIO start button on pin %d (%s, v2 ioctl polling, falling edge)", b.pin, line.Chip)

	lastVal := 1 // button pulled up = 1
	for {
		val, err := line.Read()
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if lastVal == 1 && val == 0 {
			zone := b.detectZone()
			arena := b.detectArena()
			log.Printf("Start button pressed, zone=%s arena=%s", zone, arena)
			if b.onStart != nil {
				b.onStart(zone, arena)
			}
			time.Sleep(200 * time.Millisecond) // debounce
		}
		lastVal = val
		time.Sleep(50 * time.Millisecond)
	}
}

// detectZone reads the zone from the arena USB stick. 2027: up to 4 teams per
// round, one per corner; zone1 wins over zone2 when both are present.
func (b *Button) detectZone() string {
	for _, zone := range []string{"1", "2", "3", "4"} {
		if _, err := os.Stat(filepath.Join(b.arenaUSB, "zone"+zone+".txt")); err == nil {
			return zone
		}
	}
	return "0"
}

// detectArena reads the arena letter from the arena USB stick.
func (b *Button) detectArena() string {
	return DetectArena(b.arenaUSB)
}

// DetectArena returns the arena letter from the zone/arena files on the arena
// USB stick, defaulting to "A" when none is present.
func DetectArena(usbPath string) string {
	for _, arena := range []string{"A", "B"} {
		for _, name := range []string{"arena" + arena + ".txt", "arena" + strings.ToLower(arena) + ".txt"} {
			if _, err := os.Stat(filepath.Join(usbPath, name)); err == nil {
				return arena
			}
		}
	}
	return "A"
}
