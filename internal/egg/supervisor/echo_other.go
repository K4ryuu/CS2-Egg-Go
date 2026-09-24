// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package supervisor

import (
	"errors"
	"os"
)

// disableEcho is Linux-only; the image runs there. Dev machines keep echo.
func disableEcho(*os.File) (func(), error) {
	return nil, errors.New("echo control not supported on this platform")
}
