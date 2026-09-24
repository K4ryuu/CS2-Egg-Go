// SPDX-License-Identifier: GPL-3.0-or-later

package guard

import (
	"context"
	"io"
	"os"
)

// readKmsg streams new kernel log records to fn until ctx ends. Only lines
// written after the open are seen: the buffer is seeked past.
func readKmsg(ctx context.Context, fn func(line string)) error {
	f, err := os.Open("/dev/kmsg")
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		f.Close()
	}()
	f.Seek(0, io.SeekEnd)
	buf := make([]byte, 8192)
	for {
		n, err := f.Read(buf) // one record per read
		if n > 0 {
			fn(string(buf[:n]))
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if err == io.EOF {
				continue
			}
			// EPIPE: the ring overwrote records we had not read; keep going
			continue
		}
	}
}
