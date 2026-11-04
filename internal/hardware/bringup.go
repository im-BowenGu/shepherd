package hardware

// PiLow board bring-up.
//
// The board hangs off the brain behind a level shifter whose enable line is
// BCM GPIO18. os/scripts/PiLow_check.sh documents the sequence the original
// brain used, and skipping it is why nothing on I2C answers:
//
//	GPIO26 (start button) configured as input with pull-up; if it reads high
//	the button is not pressed, so it is safe to bring the board up, and
//	GPIO18 driven high enables the level shifter for I2C and power control.
//
// Without GPIO18 high the Pi's I2C lines never reach the board: every
// transaction fails with EREMOTEIO, the MCU never sees a brain and after
// about five minutes it shuts the power rails down and stops answering.
//
// Holding the GPIO18 line open for the lifetime of the process is the "brain
// has booted" signal, and the keepalive below keeps a little I2C traffic
// flowing so the board keeps seeing a live brain.
//
// IMPORTANT: do NOT write register 26. The robot library labels it "watchdog",
// but the real PiLow firmware implements it as PORTF.DIRCLR bit 3 - it turns
// the MCU's PF3 power line into an input, releasing it and dropping the rails.
// The original brain never wrote it either.

import (
	"errors"
	"log"
	"sync"
	"syscall"
	"time"

	"github.com/RoboConOxfordshire/shepherd/internal/gpio"
)

const (
	// LevelShifterPin is the brain GPIO that enables the board's level shifter.
	LevelShifterPin = 18
	// StartButtonPin is the PiLow start button (also the safe-mode check).
	StartButtonPin = 26

	// The MCU cuts the power rails when it goes without a brain signal for
	// too long, so keep the traffic flowing well inside its timeout.
	keepaliveInterval = 5 * time.Second
	// When a tick cannot reach the board, retry a few times promptly before
	// falling back to the normal interval.
	keepaliveRetries    = 4
	keepaliveRetryDelay = 500 * time.Millisecond
)

var (
	bringUpMu sync.Mutex
	shiftLine *gpio.Line
	boardBus  int = 1
	boardInfo struct {
		sync.Mutex
		Present     bool
		Version     int
		Initialised bool
		Failures    int
		LastError   string
	}
)

// Status reports what bring-up found, for /api/sysinfo and the UI.
type Status struct {
	LevelShifter    bool   `json:"level_shifter"`
	LevelShifterPin int    `json:"level_shifter_pin"`
	StartButtonPin  int    `json:"start_button_pin"`
	StartButtonOK   bool   `json:"start_button_ok"`
	BoardPresent    bool   `json:"board_present"`
	BoardVersion    int    `json:"board_version"`
	Initialised     bool   `json:"initialised"`
	KeepaliveFails  int    `json:"keepalive_failures"`
	LastError       string `json:"last_error"`
}

// BringUp enables the board's level shifter, probes it, and starts the
// keepalive. Safe to call more than once; later calls are no-ops while the
// first bring-up is healthy.
func BringUp(bus int) Status {
	bringUpMu.Lock()
	defer bringUpMu.Unlock()

	boardBus = bus
	st := Status{LevelShifterPin: LevelShifterPin, StartButtonPin: StartButtonPin}

	// The start button doubles as the safe-mode check: while it is held down
	// the original bring-up deliberately left the board alone. The line is
	// released immediately afterwards - the start button poller (gpio.Button)
	// must be able to claim it, and holding it here made that claim fail with
	// EBUSY.
	if btn, err := gpio.RequestInput(StartButtonPin, "shepherd-bringup"); err != nil {
		log.Printf("board bring-up: start button line %d unavailable: %v", StartButtonPin, err)
	} else if level, err := btn.Read(); err != nil {
		log.Printf("board bring-up: cannot read start button line %d: %v", StartButtonPin, err)
		btn.Close()
	} else {
		st.StartButtonOK = true
		biased := btn.Biased
		btn.Close()
		if level == 0 && !biased {
			// No pull-up on the line: a low reading only means nothing is
			// driving it, not that the button is held. The brain needs the
			// SoC pull-up for the start button (the board has none of its
			// own), and without it the level shifter would never be
			// enabled and the PiLow would power the Pi off.
			log.Printf("board bring-up: start button (GPIO%d) reads low but "+
				"the pin has no pull-up - not treating it as held",
				StartButtonPin)
		} else if level == 0 {
			log.Printf("board bring-up: start button on GPIO%d is held DOWN (safe mode) - "+
				"leaving the board level shifter disabled", StartButtonPin)
			saveStatus(st)
			return st
		}
	}

	if shiftLine == nil || shiftLine.FD < 0 {
		line, err := gpio.RequestOutput(LevelShifterPin, "shepherd-levelshift", 1)
		switch {
		case err == nil:
			shiftLine = line
			st.LevelShifter = true
			log.Printf("board bring-up: level shifter enabled on GPIO%d via %s", LevelShifterPin, line.Chip)
		case errors.Is(err, syscall.EBUSY):
			// pilowhold (our own bring-up service) already owns the line, so
			// the shifter is on. Not an error - carry on to the handshake.
			st.LevelShifter = true
			log.Printf("board bring-up: level shifter on GPIO%d already held by pilowhold", LevelShifterPin)
		default:
			// Carry on regardless: the handshake below fails harmlessly if the
			// board really is dark, whereas bailing out here used to skip board
			// initialisation even when the shifter was actually fine.
			log.Printf("board bring-up: cannot enable level shifter on GPIO%d: %v "+
				"(the board may not answer on I2C)", LevelShifterPin, err)
		}
	} else {
		st.LevelShifter = true
		// Re-assert the level: if something reset it, the board went quiet.
		if err := shiftLine.Write(1); err != nil {
			log.Printf("board bring-up: re-asserting GPIO%d failed: %v", LevelShifterPin, err)
		}
	}

	// Always attempt the handshake, even if a single probe would have missed
	// the board: InitialiseBoard retries long enough for an MCU that is still
	// booting, and the MCU cuts the rails if it never sees a brain.
	if version, ok := InitialiseBoard(boardBus); ok {
		st.BoardPresent = true
		st.BoardVersion = version
		st.Initialised = true
	}
	saveStatus(st)

	go keepalive()
	return st
}

// keepalive periodically reads the board so it keeps seeing a live brain on
// the bus, and re-asserts the level shifter if the board stops answering.
func keepalive() {
	var misses int
	for {
		time.Sleep(keepaliveInterval)

		err := kickKeepalive()
		if err == nil {
			if misses > 0 {
				log.Printf("board keepalive: board is back after %d failure(s)", misses)
				misses = 0
			}
			continue
		}

		misses++
		boardInfo.Lock()
		boardInfo.Failures = misses
		boardInfo.LastError = err.Error()
		boardInfo.Unlock()
		log.Printf("board keepalive: no answer on I2C bus %d (%d in a row): %v", boardBus, misses, err)

		// The MCU may have dropped the rails; re-assert the level shifter and
		// retry promptly rather than waiting a whole interval, because the MCU
		// cuts power once it goes too long without a kick.
		reassertLevelShifter()
		for i := 0; i < keepaliveRetries; i++ {
			time.Sleep(keepaliveRetryDelay)
			if err = kickKeepalive(); err == nil {
				log.Printf("board keepalive: board is back after %d failure(s)", misses)
				misses = 0
				break
			}
		}
	}
}

// kickKeepalive touches the board to keep it alive. On the real firmware
// register 26 controls PF3 (power) not a watchdog timer — writing it can
// cause the MCU to release PF3 and cut rails. The original brain relied on
// holding the level shifter (GPIO18) and normal I2C traffic to signal the
// board is alive. So we just probe it via a simple read of Version; that
// counts as liveness on the I2C bus. Any error means the board did not
// answer.
func kickKeepalive() error {
	b, err := Open(boardBus)
	if err != nil {
		return err
	}
	defer b.Close()

	version := b.Version()
	// Do NOT write to WatchdogReg here — it can disable the board on real firmware.

	boardInfo.Lock()
	boardInfo.Present = true
	boardInfo.Version = version
	boardInfo.Failures = 0
	boardInfo.LastError = ""
	boardInfo.Unlock()
	return nil
}

// reassertLevelShifter drives the level shifter enable line high again.
func reassertLevelShifter() {
	bringUpMu.Lock()
	defer bringUpMu.Unlock()
	if shiftLine != nil && shiftLine.FD >= 0 {
		if err := shiftLine.Write(1); err != nil {
			log.Printf("board keepalive: re-asserting GPIO%d failed: %v", LevelShifterPin, err)
		}
	}
}

func saveStatus(st Status) {
	boardInfo.Lock()
	boardInfo.Failures = st.KeepaliveFails
	boardInfo.Initialised = st.Initialised
	boardInfo.Unlock()
}

// Status returns the latest board bring-up state.
func BoardStatus() Status {
	bringUpMu.Lock()
	levelShifter := shiftLine != nil && shiftLine.FD >= 0
	bringUpMu.Unlock()

	boardInfo.Lock()
	defer boardInfo.Unlock()
	return Status{
		LevelShifter:    levelShifter,
		LevelShifterPin: LevelShifterPin,
		StartButtonPin:  StartButtonPin,
		BoardPresent:    boardInfo.Present,
		BoardVersion:    boardInfo.Version,
		Initialised:     boardInfo.Initialised,
		KeepaliveFails:  boardInfo.Failures,
		LastError:       boardInfo.LastError,
	}
}
