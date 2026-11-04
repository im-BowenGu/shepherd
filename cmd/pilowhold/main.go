// Command pilowhold performs the PiLow board bring-up that has to happen
// before anything touches the board's I2C bus, and then holds it for the
// lifetime of the process.
//
// Ported from the stock brain's /usr/local/bin/pilow_check.sh:
//
//	GPIO26  PI_Start button, input with pull-up. Held low = safe mode: leave
//	        the board isolated.
//	GPIO18  level-shifter output enable. Must be held HIGH or the Pi's I2C
//	        lines never reach the board and the PiLow cannot see nPIRUNNING.
//
// Why this exists separately from shepherd: the PiLow powers the Pi, and it
// only stays up while it can see nPIRUNNING asserted through the level
// shifter. If shepherd is the only thing that drives GPIO18 and shepherd
// fails, the board goes dark and the brain becomes unreachable. Running this
// as its own supervised service keeps the level shifter up independently, so
// the PiLow keeps the Pi powered and the board stays available for debugging.
//
// It is also a guardian: if shepherd owns the line, or the claim is lost when
// shepherd exits, this keeps retrying and takes over.
package main

import (
	"errors"
	"log"
	"syscall"
	"time"

	"github.com/RoboConOxfordshire/shepherd/internal/gpio"
)

const (
	startButtonPin  = 26
	levelShifterPin = 18

	// How often to re-assert the line while holding it, and how long to wait
	// before trying again when somebody else owns it.
	reassertEvery = 30 * time.Second
	retryEvery    = 2 * time.Second
	// How often to look again while waiting in safe mode.
	recheckEvery = 2 * time.Second
)

func main() {
	log.SetPrefix("pilowhold: ")
	log.SetFlags(0)

	// The start button doubles as the safe-mode check: while it is held down
	// the board must be left alone. Decide once, and stay in safe mode until
	// the button is *seen* to be released - a later failed read (the line is
	// then owned by shepherd's button poller) must not turn the decision
	// around, or the board would come up in spite of the button.
	held, known := false, false
	for {
		h, k := readStartButton()
		switch {
		case k:
			held, known = h, true
			if !held {
				// Button released: carry on.
				goto proceed
			}
			log.Printf("start button (GPIO%d) held down: safe mode, the level "+
				"shifter stays disabled until it is released", startButtonPin)
		case !known:
			// We could not read the line at all, so we cannot claim the
			// button is held. Carry on rather than bricking the board.
			goto proceed
		}
		time.Sleep(recheckEvery)
	}
proceed:

	holdLevelShifter()
}

// readStartButton samples the start button. "known" is false when the line
// could not be claimed at all, which means somebody else (shepherd's button
// poller) owns it and it must not be held for long.
func readStartButton() (held, known bool) {
	btn, err := gpio.RequestInput(startButtonPin, "pilowhold-start")
	if err != nil {
		log.Printf("start button (GPIO%d) unavailable: %v", startButtonPin, err)
		return false, false
	}
	defer btn.Close()

	level, err := btn.Read()
	if err != nil {
		log.Printf("cannot read start button (GPIO%d): %v", startButtonPin, err)
		return false, false
	}
	if level == 0 && !btn.Biased {
		// The line has no pull-up, so a low reading only means "nothing is
		// driving it" - the board has no pull-up of its own for the start
		// button, the SoC pin does. Do not mistake that for a held button:
		// the original brain set the pull-up explicitly ("raspi-gpio set 26
		// ip pu") and without it the line floats and the board would never
		// come up.
		log.Printf("start button (GPIO%d) reads low but the pin has no "+
			"pull-up, so the reading is meaningless - not treating it as "+
			"a held button", startButtonPin)
		return false, true
	}
	return level == 0, true
}

// holdLevelShifter claims GPIO18 and drives it high, retrying forever until it
// wins the line, then re-asserts it periodically.
func holdLevelShifter() {
	for {
		line, err := gpio.RequestOutput(levelShifterPin, "pilowhold-levelshift", 1)
		if err != nil {
			if errors.Is(err, syscall.EBUSY) {
				// shepherd (or the previous instance) owns the line. That is
				// fine - the shifter is on. Keep checking in case it goes away.
				time.Sleep(retryEvery)
				continue
			}
			log.Printf("cannot enable level shifter on GPIO%d: %v (retrying)", levelShifterPin, err)
			time.Sleep(retryEvery)
			continue
		}

		log.Printf("level shifter enabled on GPIO%d via %s", levelShifterPin, line.Chip)
		t := time.NewTicker(reassertEvery)
		for range t.C {
			if err := line.Write(1); err != nil {
				log.Printf("re-asserting GPIO%d failed: %v (reclaiming)", levelShifterPin, err)
				break
			}
		}
		t.Stop()
		line.Close()
	}
}
