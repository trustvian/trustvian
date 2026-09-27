//go:build linux

package main

// Opening a pseudo-terminal on Linux, with stdlib ioctls.
//
// Two ioctls: clear the lock, then read the slave's number. The constants are in
// syscall, so no dependency is added for a test that otherwise could not exist.

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("opening /dev/ptmx: %w", err)
	}
	defer func() {
		if err != nil {
			master.Close()
		}
	}()

	unlock := int32(0)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(),
		uintptr(syscall.TIOCSPTLCK), uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		return nil, nil, fmt.Errorf("unlocking the pty: %w", errno)
	}

	var number int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(),
		uintptr(syscall.TIOCGPTN), uintptr(unsafe.Pointer(&number))); errno != 0 {
		return nil, nil, fmt.Errorf("reading the pty number: %w", errno)
	}

	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number),
		os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("opening the pty slave: %w", err)
	}
	return master, slave, nil
}
