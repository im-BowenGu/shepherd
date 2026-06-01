package hardware

import (
	"fmt"
	"syscall"
)

// i2cDevice wraps Linux /dev/i2c-N syscalls.
type i2cDevice struct {
	fd int
}

func openI2C(bus int) (*i2cDevice, error) {
	fd, err := syscall.Open(fmt.Sprintf("/dev/i2c-%d", bus), syscall.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/i2c-%d: %w", bus, err)
	}
	return &i2cDevice{fd: fd}, nil
}

func (d *i2cDevice) Close() error {
	return syscall.Close(d.fd)
}

func (d *i2cDevice) selectAddr(addr byte) error {
	return ioctl(uintptr(d.fd), I2C_SLAVE, uintptr(addr))
}

func (d *i2cDevice) readByte(reg byte) (byte, error) {
	buf := []byte{reg}
	if _, err := syscall.Write(d.fd, buf); err != nil {
		return 0, err
	}
	if _, err := syscall.Read(d.fd, buf); err != nil {
		return 0, err
	}
	return buf[0], nil
}

func (d *i2cDevice) readWord(reg byte) (uint16, error) {
	buf := []byte{reg}
	if _, err := syscall.Write(d.fd, buf); err != nil {
		return 0, err
	}
	out := make([]byte, 2)
	if _, err := syscall.Read(d.fd, out); err != nil {
		return 0, err
	}
	return uint16(out[0]) | uint16(out[1])<<8, nil
}

func (d *i2cDevice) writeByte(reg, val byte) error {
	buf := []byte{reg, val}
	_, err := syscall.Write(d.fd, buf)
	return err
}

func (d *i2cDevice) writeWord(reg byte, val uint16) error {
	buf := []byte{reg, byte(val & 0xff), byte(val >> 8)}
	_, err := syscall.Write(d.fd, buf)
	return err
}

const I2C_SLAVE = 0x0703

func ioctl(fd, cmd, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, cmd, arg)
	if errno != 0 {
		return errno
	}
	return nil
}
