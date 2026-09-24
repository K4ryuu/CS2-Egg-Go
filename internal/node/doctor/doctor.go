// SPDX-License-Identifier: GPL-3.0-or-later

// Package doctor is the check framework: modules return checks, main
// prints them the way the bash doctors did and turns them into an exit code.
package doctor

import (
	"fmt"
	"io"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// Status of one check.
type Status int

const (
	OK Status = iota
	Warn
	Fail
	Fixed
)

// Check is one line of the report.
type Check struct {
	Status  Status
	Section string
	Text    string
}

// Helpers keep call sites short.
func Ok(section, text string) Check    { return Check{OK, section, text} }
func Warnf(section, text string) Check { return Check{Warn, section, text} }
func Failf(section, text string) Check { return Check{Fail, section, text} }
func Fixedf(section, text string) Check {
	return Check{Fixed, section, text}
}

// Print writes the report grouped by section, the summary and the verdict
// line, and returns the exit code (1 when any check failed).
func Print(w io.Writer, title string, checks []Check) int {
	ui.Headline(w, title)
	var warns, fails, fixed int
	section := ""
	for _, c := range checks {
		if c.Section != section {
			section = c.Section
			ui.Section(w, section)
		}
		switch c.Status {
		case OK:
			fmt.Fprintf(w, "  %s     %s\n", ui.Green("OK"), c.Text)
		case Warn:
			fmt.Fprintf(w, "  %s   %s\n", ui.Yellow("WARN"), c.Text)
			warns++
		case Fail:
			fmt.Fprintf(w, "  %s   %s\n", ui.Red("FAIL"), c.Text)
			fails++
		case Fixed:
			fmt.Fprintf(w, "  %s  %s\n", ui.Cyan("FIXED"), c.Text)
			fixed++
		}
	}
	ui.Section(w, "Summary")
	fmt.Fprintf(w, "  %s, %s, %s\n\n", ui.Yellow(fmt.Sprintf("%d warning(s)", warns)), ui.Red(fmt.Sprintf("%d failure(s)", fails)), ui.Cyan(fmt.Sprintf("%d auto-fixed", fixed)))
	switch {
	case fails > 0:
		ui.Error(w, "Doctor found %d blocking issue(s) - fix them with the commands above, then re-run doctor", fails)
		return 1
	case fixed > 0:
		ui.Ok(w, "Doctor applied %d fix(es) - re-run doctor to confirm everything is green", fixed)
	default:
		ui.Ok(w, "Everything looks healthy")
	}
	return 0
}
