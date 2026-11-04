package hardware

// Board initialisation: the one-shot "the brain has booted" signal.
//
// The PiLow MCU wants to be told the brain is up before it reports itself as
// booted. That is separate from the keepalive in bringup.go: the keepalive
// merely keeps I2C traffic flowing, while initialisation is the single
// sequence that puts the board into the state the brain owns (motors off,
// pins safe, accessories off) - the same set of writes the legacy brain made
// at startup via robot.reset.reset().
//
// We deliberately do NOT write register 26 here. The robot library labels it
// "watchdog", but the real PiLow firmware implements it as PORTF.DIRCLR bit 3:
// it turns the MCU's PF3 power line into an input, releasing it and dropping
// the power rails. The original brain never wrote it.

import (
	"log"
	"time"
)

// WatchdogReg is the MCU register that controlled PF3 on real firmware.
// Writing it can switch PF3 to input and drop power rails; do NOT write this.
// The original brain never used it. Kept for compatibility.
const WatchdogReg = 26

// InitialiseBoard performs the brain's startup handshake with the MCU. It
// retries reaching the board for a bounded window (the MCU can be booting just
// after the level shifter is asserted) and returns the firmware version and
// whether the handshake completed.
//
// Called with bringUpMu already held (from BringUp), so it does not take it.
func InitialiseBoard(bus int) (int, bool) {
	// The level shifter may be held by pilowhold rather than by us (it wins
	// the gpiod claim or we get EBUSY). That does not mean the board is
	// unreachable: pilowhold holds the OE line high for the whole session.
	// Never skip the handshake just because we don't own the line; probe
	// I2C regardless and let the retry loop fail harmlessly if the board
	// is genuinely dark.
	if shiftLine == nil || shiftLine.FD < 0 {
		log.Printf("board init: level shifter not held by us (pilowhold may hold it); probing I2C anyway")
	}

	// The MCU cuts the power rails when it never sees a brain, and the board
	// can be briefly unreachable while its own MCU is still booting after the
	// level shifter goes high. A single missed read used to be fatal because
	// nothing retried the handshake before the keepalive, so keep trying for a
	// bounded window and re-assert the level shifter as we go.
	const (
		settleDelay = 200 * time.Millisecond
		retryDelay  = 250 * time.Millisecond
		retryWindow = 15 * time.Second
	)
	time.Sleep(settleDelay)

	var b *Board
	deadline := time.Now().Add(retryWindow)
	for attempt := 0; ; attempt++ {
		var err error
		b, err = Open(bus)
		if err == nil {
			break
		}

		boardInfo.Lock()
		boardInfo.Present = false
		boardInfo.LastError = err.Error()
		boardInfo.Unlock()

		if time.Now().After(deadline) {
			log.Printf("board init: cannot reach board on I2C bus %d after %s: %v",
				bus, retryWindow, err)
			return 0, false
		}
		log.Printf("board init: board not answering on I2C bus %d (attempt %d), retrying: %v",
			bus, attempt+1, err)
		if shiftLine != nil && shiftLine.FD >= 0 {
			if werr := shiftLine.Write(1); werr != nil {
				log.Printf("board init: re-asserting GPIO%d failed: %v", LevelShifterPin, werr)
			}
		}
		time.Sleep(retryDelay)
	}
	defer b.Close()

	version := b.Version()
	log.Printf("board init: firmware v%d reporting in, sending started signal", version)

	// 1. The MCU takes "brain is running" as the level shifter (GPIO18) being
	//    held high and seeing I2C traffic. Do NOT write to reg 26 on real
	//    firmware — it causes PF3 to drop to input and can kill power rails.
	//    Just log that we've established I2C presence.
	log.Printf("board init: established I2C comms with PiLow (v%d); skipping reg26 write", version)

	// 2. Put the board into the brain-owned safe state: motors stopped, pins
	//    back to their defaults, accessories and LED off. On the legacy brain
	//    this is what robot.reset.reset() did at startup.
	if err := Reset(bus); err != nil {
		log.Printf("board init: reset to safe state failed: %v", err)
	} else {
		log.Printf("board init: board initialised (motors off, pins safe, accessories off)")
	}

	// 3. Record what the MCU reports back, so "not reporting booted" is
	//    visible in the log rather than guessed at.
	DumpRegisters(bus)

	boardInfo.Lock()
	boardInfo.Present = true
	boardInfo.Version = version
	boardInfo.Initialised = true
	boardInfo.LastError = ""
	boardInfo.Unlock()
	return version, true
}

// DumpRegisters logs the MCU state registers the brain cares about.
func DumpRegisters(bus int) {
	b, err := Open(bus)
	if err != nil {
		log.Printf("board dump: %v", err)
		return
	}
	defer b.Close()

	regs := []struct {
		name string
		reg  byte
	}{
		{"version", RegVersion},
		{"enable_motors", RegEnableMotors},
		{"enable_motor_pwr", RegEnableMotorPwr},
		{"enable_12v_acc", RegEnable12VAcc},
		{"enable_5v_acc", RegEnable5VAcc},
		{"motor_error", RegMotorErrorState},
		{"system_error", RegSysErrorState},
	}

	line := "board state:"
	for _, r := range regs {
		v, err := b.readReg(r.reg)
		if err != nil {
			line += " " + r.name + "=ERR"
			continue
		}
		line += " " + r.name + "=" + itoa(int(v))
	}
	log.Print(line)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
