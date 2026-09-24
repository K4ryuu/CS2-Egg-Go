// SPDX-License-Identifier: GPL-3.0-or-later

package core

import (
	"os"
	"strconv"
)

// listenPath names a socket inside an open directory through /proc, so the
// kernel resolves it against that very directory, whatever the volume path
// turns into meanwhile.
func listenPath(dir *os.File, name string) string {
	return "/proc/self/fd/" + strconv.Itoa(int(dir.Fd())) + "/" + name
}
