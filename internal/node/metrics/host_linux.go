// SPDX-License-Identifier: GPL-3.0-or-later

package metrics

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// hostCPU reads /proc/stat: busy and total jiffies since boot, plus the
// core count. Two reads give a usage delta.
func hostCPU() (busy, total uint64, cores int, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			cores++
			continue
		}
		var vals []uint64
		for _, fv := range fields[1:] {
			n, _ := strconv.ParseUint(fv, 10, 64)
			vals = append(vals, n)
		}
		for i, v := range vals {
			total += v
			if i != 3 && i != 4 { // idle, iowait
				busy += v
			}
		}
		ok = true
	}
	return busy, total, cores, ok
}

// hostMem reads /proc/meminfo: used (total minus available) and total.
func hostMem() (used, total uint64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	var avail uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		kb, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = kb << 10
		case "MemAvailable:":
			avail = kb << 10
		}
	}
	if total == 0 {
		return 0, 0, false
	}
	return total - avail, total, true
}
