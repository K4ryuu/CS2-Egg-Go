// SPDX-License-Identifier: GPL-3.0-or-later

// Package addons keeps Metamod, CounterStrikeSharp, SwiftlyS2 and ModSharp
// installed and current from their GitHub releases.
package addons

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// APIBase is overridden by tests.
var APIBase = "https://api.github.com"

// Asset is one downloadable file of a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Release is the part of a GitHub release the updaters use.
type Release struct {
	Tag        string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
	// Pinned marks a release the node picked by version instead of "the
	// newest one": the egg then installs it even if that means going back.
	Pinned bool `json:"-"`
}

// The repos behind the four addons, named once so a version pin and an
// updater can never drift apart.
const (
	RepoMetamod  = "alliedmodders/metamod-source"
	RepoCSS      = "roflmuffin/CounterStrikeSharp"
	RepoSwiftly  = "swiftly-solution/swiftlys2"
	RepoModSharp = "Kxnrl/modsharp-public"
)

// Pinnable maps the name a version pin uses to the repo that serves it.
var Pinnable = map[string]string{
	"metamod":  RepoMetamod,
	"css":      RepoCSS,
	"swiftly":  RepoSwiftly,
	"modsharp": RepoModSharp,
}

// KeyForRepo is the pin name of a repo, empty when it cannot be pinned.
func KeyForRepo(repo string) string {
	for key, r := range Pinnable {
		if r == repo {
			return key
		}
	}
	return ""
}

// ErrRateLimited: GitHub answered 403 or 429. Every server on a node shares
// one IP, so this happens; the installed version keeps running.
var ErrRateLimited = errors.New("github api rate limited")

// get calls the GitHub API and separates a rate limit from a real failure.
func get(ctx context.Context, client *http.Client, url, repo string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cs2egg")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w (HTTP %d)", ErrRateLimited, resp.StatusCode)
	default:
		return nil, fmt.Errorf("github api returned HTTP %d for %s", resp.StatusCode, repo)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// FetchRelease returns the latest stable release, or with prerelease the
// newest release of any kind (first element of /releases).
func FetchRelease(ctx context.Context, client *http.Client, repo string, prerelease bool) (*Release, error) {
	if prerelease {
		list, err := FetchReleases(ctx, client, repo)
		if err != nil {
			return nil, err
		}
		return &list[0], nil
	}
	body, err := get(ctx, client, APIBase+"/repos/"+repo+"/releases/latest", repo)
	if err != nil {
		return nil, err
	}
	var rel Release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("parse release: %w", err)
	}
	return &rel, nil
}

// FetchReleases lists a repo's releases, newest first.
func FetchReleases(ctx context.Context, client *http.Client, repo string) ([]Release, error) {
	body, err := get(ctx, client, APIBase+"/repos/"+repo+"/releases", repo)
	if err != nil {
		return nil, err
	}
	var list []Release
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("parse releases: %w", err)
	}
	if len(list) == 0 {
		return nil, errors.New("no releases")
	}
	return list, nil
}

// FetchReleaseTag returns one release by its exact tag.
func FetchReleaseTag(ctx context.Context, client *http.Client, repo, tag string) (*Release, error) {
	body, err := get(ctx, client, APIBase+"/repos/"+repo+"/releases/tags/"+url.PathEscape(tag), repo)
	if err != nil {
		return nil, err
	}
	var rel Release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("parse release: %w", err)
	}
	return &rel, nil
}

// Holds reports whether the release carries the wanted version, in its tag
// or in an asset name (Metamod's build number only appears there).
func (r *Release) Holds(want string) bool {
	if r.Tag == want || strings.Contains(r.Tag, want) {
		return true
	}
	for _, a := range r.Assets {
		if strings.Contains(a.Name, want) {
			return true
		}
	}
	return false
}

// Asset returns the first asset whose name matches pattern.
func (r *Release) Asset(pattern *regexp.Regexp) (Asset, bool) {
	for _, a := range r.Assets {
		if pattern.MatchString(a.Name) {
			return a, true
		}
	}
	return Asset{}, false
}
