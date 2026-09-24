// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package ui

import "golang.org/x/sys/unix"

const termiosReq = unix.TIOCGETA
const termiosSet = unix.TIOCSETA
