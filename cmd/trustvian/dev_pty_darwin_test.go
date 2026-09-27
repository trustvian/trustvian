//go:build darwin

package main

// Opening a pseudo-terminal on darwin, with stdlib ioctls.
//
// Three ioctls, in the order posix_openpt(3) performs them: grant the slave to
// this user, unlock it, then ask for its name. The constants are in syscall, so
// no dependency is added for a test that otherwise could not exist.

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

	if err := ioctlNoArg(master.Fd(), syscall.TIOCPTYGRANT); err != nil {
		return nil, nil, fmt.Errorf("granting the pty: %w", err)
	}
	if err := ioctlNoArg(master.Fd(), syscall.TIOCPTYUNLK); err != nil {
		return nil, nil, fmt.Errorf("unlocking the pty: %w", err)
	}

	var name [128]byte
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(),
		uintptr(syscall.TIOCPTYGNAME), uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		return nil, nil, fmt.Errorf("reading the pty name: %w", errno)
	}

	slave, err = os.OpenFile(cString(name[:]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("opening the pty slave: %w", err)
	}
	return master, slave, nil
}

func ioctlNoArg(fd uintptr, request uint) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(request), 0); errno != 0 {
		return errno
	}
	return nil
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
