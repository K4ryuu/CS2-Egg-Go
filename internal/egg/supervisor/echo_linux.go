// SPDX-License-Identifier: GPL-3.0-or-later

package supervisor

import (
	"os"

	"golang.org/x/sys/unix"
)

// disableEcho clears ECHO on the terminal behind f and returns a restore
// function. Non-terminals return an error and nothing changes.
func disableEcho(f *os.File) (func(), error) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	t := *old
	t.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &t); err != nil {
		return nil, err
	}
	return func() { unix.IoctlSetTermios(fd, unix.TCSETS, old) }, nil
}
