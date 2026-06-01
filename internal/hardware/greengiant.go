package hardware

import "fmt"

// GreenGiant I2C register map (address 0x08).
const (
	GGI2CAddr = 0x08

	RegServoPWMBase    = 0x01
	RegGPIOPWMBase     = 0x30
	RegAnalogStart     = 0x00
	RegControlStart    = 0x08
	RegDigitalStart    = 0x0C
	RegUserLED         = 0x19
	RegEnableMotors    = 0x1B
	RegBatteryVH       = 0x1C
	RegBatteryVL       = 0x1D
	RegFVRH            = 0x1E
	RegFVRL            = 0x1F
	RegVersion         = 0x20
	RegEnable12VAcc    = 0x21
	RegEnable5VAcc     = 0x22
	RegEnableMotorPwr  = 0x23
	RegMotorMagStart   = 0x24 // 2 bytes per motor (A=0x24, B=0x26)
	RegMotorDirStart   = 0x28 // 1 byte per motor direction
	RegMotorErrorState = 0x2A
	RegSysErrorState   = 0x2B
)

// Pin count constants.
const (
	NumPins     = 4
	NumMotors   = 2
	ServoCount  = 4
)

// Pin modes (written to control register).
const (
	ModeOutput      = 0b000
	ModeInput       = 0b001
	ModeInputAnalog = 0b010
	ModeInputPullUp = 0b011
	ModePWMServo    = 0b100
	ModeTimer       = 0b110
)

// Default PWM centres and ranges.
const (
	GGPwmCenter     = 374
	GGPwmHalfRange  = 224
	PiLowPwmCenter  = 4500
	PiLowPwmHalfRange = 2100
)

// Board represents the GreenGiant or PiLow board.
type Board struct {
	dev     *i2cDevice
	version int
}

// Pin represents a single GPIO/PWM/Servo pin.
type Pin struct {
	board   *Board
	index   int // 0-3
}

// Motor represents a single motor channel.
type Motor struct {
	board *Board
	index int // 0-1
}

// Open opens the I2C bus and detects the board version.
func Open(bus int) (*Board, error) {
	dev, err := openI2C(bus)
	if err != nil {
		return nil, err
	}
	if err := dev.selectAddr(GGI2CAddr); err != nil {
		dev.Close()
		return nil, fmt.Errorf("select i2c addr: %w", err)
	}

	b := &Board{dev: dev}
	v, err := dev.readByte(RegVersion)
	if err != nil {
		dev.Close()
		return nil, fmt.Errorf("read version: %w", err)
	}
	b.version = int(v)
	return b, nil
}

// Close closes the I2C connection.
func (b *Board) Close() error {
	return b.dev.Close()
}

// Version returns the board firmware version (<10 = GreenGiant, >=10 = PiLow).
func (b *Board) Version() int { return b.version }

// IsPiLow returns true if this is a PiLow board (version >= 10).
func (b *Board) IsPiLow() bool { return b.version >= 10 }

// --- Power Management ---

func (b *Board) readReg(reg byte) (byte, error)  { return b.dev.readByte(reg) }
func (b *Board) writeReg(reg, val byte) error { return b.dev.writeByte(reg, val) }

func (b *Board) GetBatteryVoltage() (float64, error) {
	raw, err := b.dev.readWord(RegBatteryVH)
	if err != nil {
		return 0, err
	}
	return float64(raw) / 65472.0 * 12.288, nil
}

func (b *Board) GetFVR() (float64, error) {
	if !b.IsPiLow() {
		raw, err := b.dev.readWord(RegFVRH)
		if err != nil {
			return 0, err
		}
		return float64(raw) / 65472.0 * 4.096, nil
	}
	return 0, fmt.Errorf("FVR not available on PiLow")
}

// SetLED enables or disables the user LED.
func (b *Board) SetLED(on bool) error {
	v := byte(0)
	if on {
		v = 1
	}
	return b.writeReg(RegUserLED, v)
}

// GetLED returns the current user LED state.
func (b *Board) GetLED() (bool, error) {
	v, err := b.readReg(RegUserLED)
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

// EnableMotors enables or disables motor power (also controls 12V on GG).
func (b *Board) EnableMotors(on bool) error {
	v := byte(0)
	if on {
		v = 1
	}
	return b.writeReg(RegEnableMotors, v)
}

func (b *Board) GetMotorPowerEnabled() (bool, error) {
	if b.IsPiLow() {
		v, err := b.readReg(RegEnableMotorPwr)
		if err != nil {
			return false, err
		}
		return v != 0, nil
	}
	return b.GetLED()
}

// --- Accessory Power ---

func (b *Board) Set12VAcc(on bool) error {
	if b.IsPiLow() {
		v := byte(0)
		if on {
			v = 1
		}
		return b.writeReg(RegEnable12VAcc, v)
	}
	return b.EnableMotors(on)
}

func (b *Board) Get12VAcc() (bool, error) {
	if b.IsPiLow() {
		v, err := b.readReg(RegEnable12VAcc)
		if err != nil {
			return false, err
		}
		return v != 0, nil
	}
	return b.GetLED()
}

func (b *Board) Set5VAcc(on bool) error {
	if !b.IsPiLow() {
		return nil
	}
	v := byte(0)
	if on {
		v = 1
	}
	return b.writeReg(RegEnable5VAcc, v)
}

func (b *Board) Get5VAcc() (bool, error) {
	if !b.IsPiLow() {
		return false, nil
	}
	v, err := b.readReg(RegEnable5VAcc)
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

// --- Pin Control ---

// Pin returns a handle to one of the 4 GPIO/PWM/Servo pins (index 0-3).
func (b *Board) Pin(index int) *Pin {
	if index < 0 || index >= NumPins {
		return nil
	}
	return &Pin{board: b, index: index}
}

func (p *Pin) controlReg() byte  { return RegControlStart + byte(p.index) }
func (p *Pin) digitalReg() byte  { return RegDigitalStart + byte(p.index) }
func (p *Pin) analogHReg() byte  { return RegAnalogStart + byte(p.index)*2 }
func (p *Pin) pwmHReg() byte     { return RegServoPWMBase + byte(p.index)*2 }

func (p *Pin) IsPiLow() bool { return p.board.IsPiLow() }

func (p *Pin) SetMode(mode byte) error {
	return p.board.writeReg(p.controlReg(), mode)
}

func (p *Pin) GetMode() (byte, error) {
	return p.board.readReg(p.controlReg())
}

func (p *Pin) SetDigital(high bool) error {
	v := byte(0)
	if high {
		v = 1
	}
	return p.board.writeReg(p.digitalReg(), v)
}

func (p *Pin) GetDigital() (bool, error) {
	v, err := p.board.readReg(p.digitalReg())
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

func (p *Pin) GetAnalog() (uint16, error) {
	return p.board.dev.readWord(p.analogHReg())
}

func (p *Pin) SetPWM(value uint16) error {
	if p.IsPiLow() {
		return p.board.dev.writeWord(p.pwmHReg()+GPIO_PWM_OFFSET, value)
	}
	return p.board.dev.writeWord(p.pwmHReg(), value)
}

// GPIO_PWM_OFFSET is the offset from servo PWM base to GPIO PWM base on PiLow.
const GPIO_PWM_OFFSET = RegGPIOPWMBase - RegServoPWMBase

// --- Motor Control ---

// Motor returns a handle to a motor channel (index 0-1).
func (b *Board) Motor(index int) *Motor {
	if index < 0 || index >= NumMotors {
		return nil
	}
	return &Motor{board: b, index: index}
}

func (m *Motor) magReg() byte  { return RegMotorMagStart + byte(m.index)*2 }
func (m *Motor) dirReg() byte  { return RegMotorDirStart + byte(m.index) }

// SetPower sets motor power (-100 to 100). Negative = reverse.
func (m *Motor) SetPower(pct int) error {
	if pct < -100 {
		pct = -100
	} else if pct > 100 {
		pct = 100
	}

	dir := byte(0)
	mag := byte(0)
	if pct >= 0 {
		dir = 1
		mag = byte(pct * 255 / 100)
	} else {
		dir = 0
		mag = byte(-pct * 255 / 100)
	}

	if err := m.board.writeReg(m.dirReg(), dir); err != nil {
		return err
	}
	return m.board.dev.writeWord(m.magReg(), uint16(mag))
}

func (m *Motor) Stop() error {
	return m.SetPower(0)
}

// --- Ultrasonic Sensor ---

// Ultrasonic handles an HC-SR04 via TIMER mode on a PiLow pin.
type Ultrasonic struct {
	trigger *Pin
	echo    *Pin
}

func NewUltrasonic(trigger, echo *Pin) *Ultrasonic {
	return &Ultrasonic{trigger: trigger, echo: echo}
}

// Distance returns the distance in centimetres. Not implemented for GG.
func (u *Ultrasonic) Distance() (float64, error) {
	return 0, fmt.Errorf("ultrasonic not implemented on GG")
}

// --- Global Reset ---

// Reset stops all motors, disables servos, turns off accessories and LED.
func Reset(bus int) error {
	b, err := Open(bus)
	if err != nil {
		return fmt.Errorf("open board: %w", err)
	}
	defer b.Close()

	// Stop motors
	for i := 0; i < NumMotors; i++ {
		if err := b.Motor(i).Stop(); err != nil {
			return fmt.Errorf("stop motor %d: %w", i, err)
		}
	}

	// Set all pins to safe state
	for i := 0; i < NumPins; i++ {
		p := b.Pin(i)
		if b.IsPiLow() {
			p.SetMode(ModeOutput)
			p.SetDigital(false)
		} else {
			p.SetPWM(0)
		}
	}

	b.SetLED(false)
	b.EnableMotors(false)
	b.Set12VAcc(false)
	b.Set5VAcc(false)

	return nil
}
