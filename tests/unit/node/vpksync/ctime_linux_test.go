// SPDX-License-Identifier: GPL-3.0-or-later

package vpksync_test

import "syscall"

func statCtime(st *syscall.Stat_t) syscall.Timespec { return st.Ctim }
