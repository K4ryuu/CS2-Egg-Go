// SPDX-License-Identifier: GPL-3.0-or-later

// Package module is the seam every node feature plugs into. Modules are
// compiled in, switched on by their config section, and only ever talk to
// the core.
package module

import (
	"context"
	"io"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
)

// Kind of a wizard answer.
type Kind int

const (
	Text Kind = iota
	Int
	Bool
	Choice
	List // comma-separated values, each one of Choices when Choices is set
)

// Question is one wizard prompt for a module setting. Key is the JSON key
// in the module's config section.
type Question struct {
	Key      string
	Prompt   string
	Help     string
	Default  string
	Kind     Kind
	Choices  []Option // for Choice and List
	Required bool     // an empty answer is refused; with --yes the module stays off
	// When, if set, decides from the answers so far whether to ask at all; a
	// skipped question keeps its existing or default value.
	When func(answers map[string]any) bool
}

// Option is one allowed value with its one-line meaning.
type Option struct {
	Value string
	Desc  string
}

// Noter is a module with something the operator should know once it is
// switched on: a panel variable to set, a plugin setting that fights it.
// The installer prints these right after the module's questions.
type Noter interface{ Notes() []string }

// Seeder is a module with config keys the wizard does not ask about but
// that belong in the file anyway, so the operator can see and edit them.
// Existing values always win; seeding only fills what is missing.
type Seeder interface{ Seed() map[string]any }

// Statuser renders its own section of `cs2node status` from the snapshot
// the daemon answered with. A module without one shows no section, which
// is why metrics has none.
type Statuser interface {
	PrintStatus(w io.Writer, c *core.Core, st core.Status)
}

// Module is one node feature.
type Module interface {
	// Name is the config section and the name advertised to eggs.
	Name() string
	// Describe is the one-liner the installer shows.
	Describe() string
	// Questions returns the wizard prompts; the installer pre-fills Default
	// from the existing config section when there is one.
	Questions() []Question
	// Run blocks until ctx ends.
	Run(ctx context.Context, c *core.Core) error
	// Doctor checks this module's health; c may have no running Core loop.
	Doctor(ctx context.Context, c *core.Core) []doctor.Check
}
