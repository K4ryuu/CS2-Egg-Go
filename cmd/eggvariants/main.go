// SPDX-License-Identifier: GPL-3.0-or-later

// Command eggvariants regenerates the standalone (no node daemon)
// Pterodactyl egg export from the canonical, hand-maintained one. Run
// after editing pterodactyl/kitsunelab-cs2-go-egg.json.
//
//go:generate go run .
package main

import (
	"fmt"
	"os"

	"github.com/K4ryuu/CS2-Egg-Go/internal/eggvariants"
)

const (
	src = "pterodactyl/kitsunelab-cs2-go-egg.json"
	out = "pterodactyl/kitsunelab-cs2-go-egg-standalone.json"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "eggvariants:", err)
		os.Exit(1)
	}
}

func run() error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	standalone, err := eggvariants.Standalone(data)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	return os.WriteFile(out, standalone, 0o644)
}
