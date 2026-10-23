// SPDX-License-Identifier: GPL-3.0-or-later

package maintenance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
	"github.com/moby/moby/client"
)

// Result is what one task did (or, on a dry run, would do).
type Result struct {
	Task  string `json:"task"`
	Items int    `json:"items,omitempty"`
	Bytes int64  `json:"bytes,omitempty"`
	Dry   bool   `json:"dry,omitempty"`
	Note  string `json:"note,omitempty"`
	Err   string `json:"err,omitempty"`
}

// Engine is the slice of docker the tasks need; tests fake it.
type Engine interface {
	// Dangling counts untagged images, used or not. Prune decides the rest.
	Dangling(ctx context.Context) (items int, bytes int64, err error)
	PruneImages(ctx context.Context) (items int, bytes int64, err error)
	PruneBuildCache(ctx context.Context, olderThan time.Duration) (items int, bytes int64, err error)
}

// Exec runs a host command and returns its combined output.
type Exec func(ctx context.Context, name string, args ...string) ([]byte, error)

// RunCmd is the real Exec.
func RunCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type mobyEngine struct{ cli *client.Client }

// NewEngine connects to the engine through the usual environment.
func NewEngine() (Engine, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, err
	}
	return &mobyEngine{cli: cli}, nil
}

func (e *mobyEngine) Dangling(ctx context.Context) (int, int64, error) {
	res, err := e.cli.ImageList(ctx, client.ImageListOptions{Filters: make(client.Filters).Add("dangling", "true")})
	if err != nil {
		return 0, 0, err
	}
	var size int64
	for _, img := range res.Items {
		size += img.Size
	}
	return len(res.Items), size, nil
}

// PruneImages only ever touches dangling images, never `prune -a`: a tagged
// image that no container runs right now is somebody's rollback target.
func (e *mobyEngine) PruneImages(ctx context.Context) (int, int64, error) {
	res, err := e.cli.ImagePrune(ctx, client.ImagePruneOptions{Filters: make(client.Filters).Add("dangling", "true")})
	return len(res.Report.ImagesDeleted), int64(res.Report.SpaceReclaimed), err
}

func (e *mobyEngine) PruneBuildCache(ctx context.Context, olderThan time.Duration) (int, int64, error) {
	until := fmt.Sprintf("%dh", int(olderThan.Hours()))
	res, err := e.cli.BuildCachePrune(ctx, client.BuildCachePruneOptions{Filters: make(client.Filters).Add("until", until)})
	return len(res.Report.CachesDeleted), int64(res.Report.SpaceReclaimed), err
}

var reJournalSize = regexp.MustCompile(`take up ([0-9.]+)([BKMGT]?)`)

// ParseJournalSize reads `journalctl --disk-usage` ("Archived and active
// journals take up 1.2G in the file system.").
func ParseJournalSize(out string) (int64, bool) {
	m := reJournalSize.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	mult := map[string]float64{"": 1, "B": 1, "K": 1 << 10, "M": 1 << 20, "G": 1 << 30, "T": 1 << 40}
	return int64(n * mult[m[2]]), true
}

func journalSize(ctx context.Context, ex Exec) (int64, error) {
	out, err := ex(ctx, "journalctl", "--disk-usage")
	if err != nil {
		return 0, fmt.Errorf("journalctl --disk-usage: %w", err)
	}
	n, ok := ParseJournalSize(string(out))
	if !ok {
		return 0, fmt.Errorf("journalctl --disk-usage: unreadable output %q", out)
	}
	return n, nil
}

// vacuumJournal drops journal files older than days. Freed bytes are the
// difference of two disk-usage readings, because vacuum's own report is prose.
func vacuumJournal(ctx context.Context, ex Exec, days int, dry bool) Result {
	r := Result{Task: "journal", Dry: dry}
	before, err := journalSize(ctx, ex)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	if dry {
		r.Note = fmt.Sprintf("journals take up %s, would keep %d days", ui.Size(before), days)
		return r
	}
	if out, err := ex(ctx, "journalctl", fmt.Sprintf("--vacuum-time=%dd", days)); err != nil {
		r.Err = fmt.Sprintf("journalctl --vacuum-time: %v: %s", err, out)
		return r
	}
	if after, err := journalSize(ctx, ex); err == nil && before > after {
		r.Bytes = before - after
	}
	return r
}

func pruneImages(ctx context.Context, e Engine, dry bool) Result {
	r := Result{Task: "images", Dry: dry}
	var err error
	if dry {
		r.Items, r.Bytes, err = e.Dangling(ctx)
		r.Note = "dangling images; only the unused ones go on a real pass"
	} else {
		r.Items, r.Bytes, err = e.PruneImages(ctx)
	}
	if err != nil {
		r.Err = err.Error()
	}
	return r
}

func pruneBuildCache(ctx context.Context, e Engine, hours int, dry bool) Result {
	r := Result{Task: "build-cache", Dry: dry}
	if dry {
		r.Note = fmt.Sprintf("would prune build cache older than %dh", hours)
		return r
	}
	var err error
	if r.Items, r.Bytes, err = e.PruneBuildCache(ctx, time.Duration(hours)*time.Hour); err != nil {
		r.Err = err.Error()
	}
	return r
}

// RebootFile is what Debian and Ubuntu drop when an upgrade needs a reboot.
var RebootFile = "/var/run/reboot-required"

// RebootRequired reports whether an upgrade is waiting on a reboot.
func RebootRequired() bool {
	_, err := os.Stat(RebootFile)
	return err == nil
}

// FreePercent is the share of the filesystem at path that is still free.
func FreePercent(path string) (float64, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, err
	}
	if fs.Blocks == 0 {
		return 0, fmt.Errorf("%s: filesystem reports no blocks", path)
	}
	return float64(fs.Bavail) / float64(fs.Blocks) * 100, nil
}
