// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import "golang.org/x/sys/unix"

const termiosReq = unix.TCGETS
const termiosSet = unix.TCSETS
