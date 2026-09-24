// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package guard

import (
	"context"
	"errors"
)

func readKmsg(context.Context, func(string)) error {
	return errors.New("kernel log needs linux")
}
