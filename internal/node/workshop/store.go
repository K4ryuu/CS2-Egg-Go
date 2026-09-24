// SPDX-License-Identifier: GPL-3.0-or-later

package workshop

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
)

// AppID is CS2. Nothing else is cached.
const AppID = "730"

// Paths inside a server volume. CS2 and MultiAddonManager both download
// through the game's own Steam client, next to the engine binary.
const (
	VolWorkshop = "game/bin/linuxsteamrt64/steamapps/workshop"
	VolACF      = VolWorkshop + "/appworkshop_" + AppID + ".acf"
	VolContent  = VolWorkshop + "/content/" + AppID
)

// Item is one version of one workshop item: a map, a model pack, a skin
// set, whatever was published. Files holds every file of it, because a
// bigger addon ships as <id>_dir.vpk plus numbered chunks.
type Item struct {
	ID       string    `json:"id"`
	Manifest string    `json:"manifest"` // the Steam version id
	Size     int64     `json:"size"`     // as the acf records it
	Updated  int64     `json:"updated"`  // timeupdated, unix seconds
	Files    []string  `json:"files"`    // names inside the version dir
	Bytes    int64     `json:"-"`        // what those files weigh right now
	Stored   time.Time `json:"-"`        // when the node took this version over
}

// Shared lists the files servers link to (everything but the small
// bookkeeping file, which each server keeps as its own copy).
func (it Item) Shared() []string {
	var out []string
	for _, f := range it.Files {
		if f != PublishData {
			out = append(out, f)
		}
	}
	return out
}

// PublishData is the tiny descriptor Steam drops next to the payload.
const PublishData = "publish_data.txt"

// ValidManifest reports whether a version id is one we can reason about.
// Items published before Source 2 carry no manifest, and the game records
// "-1" or "0" for them: those cannot be compared with what Steam reports,
// so they are left with the server that downloaded them.
func ValidManifest(m string) bool {
	if m == "" || m == "0" || m == "-1" {
		return false
	}
	for _, r := range m {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Store is the node's copy of the workshop content.
type Store struct {
	Dir   string // /srv/cs2-workshop
	Mount string // where Dir appears inside every container
}

func (s Store) versionDir(id, manifest string) string {
	return filepath.Join(s.Dir, AppID, id, manifest)
}

// LinkTarget is what a volume's symlink points at.
func (s Store) LinkTarget(id, manifest, file string) string {
	return s.Mount + "/" + AppID + "/" + id + "/" + manifest + "/" + file
}

// Items lists every stored version, newest first per item.
func (s Store) Items() []Item {
	var out []Item
	ids, _ := os.ReadDir(filepath.Join(s.Dir, AppID))
	for _, id := range ids {
		if !id.IsDir() {
			continue
		}
		versions, _ := os.ReadDir(filepath.Join(s.Dir, AppID, id.Name()))
		for _, v := range versions {
			if !v.IsDir() {
				continue
			}
			if it, err := s.read(id.Name(), v.Name()); err == nil {
				out = append(out, it)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Updated > out[j].Updated
	})
	return out
}

// NewestAll is the freshest stored version of every item, from one walk of
// the store. Newest walks the whole store for a single id, so calling it in
// a loop reads every meta.json once per item: build this once instead.
func (s Store) NewestAll() map[string]Item {
	best := map[string]Item{}
	for _, it := range s.Items() {
		if cur, ok := best[it.ID]; !ok || newer(it, cur) {
			best[it.ID] = it
		}
	}
	return best
}

// newer reports whether a is the fresher of two stored versions. An update
// always bumps timeupdated; when two versions claim the same one, the copy
// the node took over later wins.
func newer(a, b Item) bool {
	return a.Updated > b.Updated || (a.Updated == b.Updated && a.Stored.After(b.Stored))
}

func (s Store) read(id, manifest string) (Item, error) {
	dir := s.versionDir(id, manifest)
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return Item{}, err
	}
	var it Item
	if err := json.Unmarshal(data, &it); err != nil {
		return Item{}, err
	}
	if len(it.Files) == 0 {
		return Item{}, fmt.Errorf("%s/%s: no files recorded", id, manifest)
	}
	if st, err := os.Stat(filepath.Join(dir, "meta.json")); err == nil {
		it.Stored = st.ModTime()
	}
	for _, f := range it.Files {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			return Item{}, err // a half-written version is not usable
		}
		if f != PublishData {
			it.Bytes += st.Size() // the descriptor is per server, not shared
		}
	}
	return it, nil
}

// Has reports whether a version is stored with all of its files.
func (s Store) Has(id, manifest string) bool {
	_, err := s.read(id, manifest)
	return err == nil
}

// take moves an item's files out of the volume into the store.
//
// Every read of the volume goes through root, never through a path built
// with filepath.Join. The files were checked with root.Lstat when they were
// enumerated, and a container can swap a checked regular file for a symlink
// before this runs: os.Open and os.Chmod would follow it, and the store is
// bind-mounted read-only into every container on the node, so following one
// to /etc/shadow would hand the attacker a host file to read. os.Root
// refuses a link that leaves the volume, which is what makes the check and
// the use agree.
func (s Store) take(root *os.Root, it Item) error {
	dir := s.versionDir(it.ID, it.Manifest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range it.Files {
		rel := path.Join(VolContent, it.ID, f)
		dst := filepath.Join(dir, f)
		if err := copyOut(root, rel, dst); err != nil {
			if f == PublishData {
				continue // tiny and per-server, not worth failing the sync
			}
			return err
		}
		// read through a read-only mount by every server: world-readable
		os.Chmod(dst, 0o644)
		if f != PublishData {
			root.Remove(rel) // the volume gets a link back in its place
		}
	}
	meta, err := json.MarshalIndent(it, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o644)
}

// Report is what one pass over a volume did.
type Report struct {
	Absorbed []Item // moved into the store, the volume now links
	Linked   []Item // already stored, the volume's copies replaced by links
	Seeded   []Item // handed to a server that never had them
	Released []Item // handed back as real files so the server can update
	Freed    int64  // bytes the volume no longer holds
}

// Sync brings one volume in line with the store: every real addon it holds
// goes into the store and becomes links, every stale link becomes a real
// file again, and (when seed is set) everything else in the store is
// offered to the server. The container must not be running.
func (s Store) Sync(volume string, owner core.Owner, seed bool, stale map[string]bool) (Report, error) {
	var rep Report
	root, err := os.OpenRoot(volume)
	if err != nil {
		return rep, err
	}
	defer root.Close()
	// one walk of the store for the whole sync: Newest walks it per id, so
	// asking inside the loop reads every meta.json once per stored item
	newest := s.NewestAll()
	acf, err := readACF(root)
	if err != nil {
		return rep, err
	}
	installed := acf.Ensure("AppWorkshop").Ensure("WorkshopItemsInstalled")
	details := acf.Ensure("AppWorkshop").Ensure("WorkshopItemDetails")

	ids := itemIDs(root)
	for _, id := range ids {
		files, links, bytes := itemFiles(root, id)
		manifest := installed.Sub(id).Str("manifest")
		payload := 0 // the descriptor stays a real file even once we own the item
		for _, f := range files {
			if f != PublishData {
				payload++
			}
		}
		// the engine's own bookkeeping is the most direct signal there is:
		// when it says a newer version exists, it is about to download one
		d := details.Sub(id)
		pending := d.Str("latest_manifest") != "" && d.Str("latest_manifest") != d.Str("manifest")
		switch {
		case payload == 0 && len(links) == 0:
			continue
		case payload == 0: // ours already
			if stale[id] || pending {
				if it, ok := newest[id]; ok {
					if err := s.release(root, it, owner); err == nil {
						rep.Released = append(rep.Released, it)
					}
				}
				continue
			}
			if it, ok := newest[id]; ok {
				if err := s.link(root, it, owner); err != nil {
					continue
				}
				if it.Manifest != manifest {
					// another server fetched a newer version and the node took
					// it over: this one follows without downloading anything,
					// and its bookkeeping has to say so
					rep.Linked = append(rep.Linked, it)
				}
			}
		default: // the server downloaded something
			if !ValidManifest(manifest) || pending {
				// a legacy item with no usable version, or an update already
				// on its way: either way, leave it with this server
				continue
			}
			it := Item{ID: id, Manifest: manifest, Files: files,
				Size:    atoi(installed.Sub(id).Str("size")),
				Updated: atoi(installed.Sub(id).Str("timeupdated"))}
			if s.Has(id, manifest) {
				stored, _ := s.read(id, manifest)
				for _, f := range it.Shared() {
					root.Remove(VolContent + "/" + id + "/" + f)
				}
				it = stored
				rep.Linked = append(rep.Linked, it)
			} else {
				if err := s.take(root, it); err != nil {
					return rep, fmt.Errorf("%s: %w", id, err)
				}
				rep.Absorbed = append(rep.Absorbed, it)
			}
			if err := s.link(root, it, owner); err != nil {
				return rep, err
			}
			rep.Freed += bytes
		}
	}

	if seed {
		have := map[string]bool{}
		for _, id := range ids {
			have[id] = true
		}
		for _, it := range s.Items() {
			if have[it.ID] || stale[it.ID] {
				continue
			}
			if newest[it.ID].Manifest != it.Manifest {
				continue // only the freshest version is offered
			}
			if err := s.link(root, it, owner); err != nil {
				continue
			}
			have[it.ID] = true
			rep.Seeded = append(rep.Seeded, it)
		}
	}

	// the acf must describe exactly what the volume now has
	for _, it := range append(append([]Item{}, rep.Absorbed...), append(rep.Linked, rep.Seeded...)...) {
		e := installed.Ensure(it.ID)
		e.SetStr("size", strconv.FormatInt(it.Size, 10))
		e.SetStr("timeupdated", strconv.FormatInt(it.Updated, 10))
		e.SetStr("manifest", it.Manifest)
		d := details.Ensure(it.ID)
		d.SetStr("manifest", it.Manifest)
		d.SetStr("timeupdated", strconv.FormatInt(it.Updated, 10))
		if d.Str("timetouched") == "" {
			d.SetStr("timetouched", strconv.FormatInt(time.Now().Unix(), 10))
		}
		d.SetStr("latest_timeupdated", strconv.FormatInt(it.Updated, 10))
		d.SetStr("latest_manifest", it.Manifest)
	}
	var total int64
	for _, id := range installed.Keys() {
		total += atoi(installed.Sub(id).Str("size"))
	}
	acf.Ensure("AppWorkshop").SetStr("SizeOnDisk", strconv.FormatInt(total, 10))
	if err := writeACF(root, acf, owner); err != nil {
		return rep, err
	}
	return rep, nil
}

// itemIDs lists the workshop items a volume has a directory for.
func itemIDs(root *os.Root) []string {
	dir, err := root.Open(VolContent)
	if err != nil {
		return nil
	}
	names, _ := dir.Readdirnames(-1)
	dir.Close()
	sort.Strings(names)
	return names
}

// itemFiles splits one item's directory into real files and links, and
// weighs the real ones. Only regular files count: a directory or a socket
// in there is none of our business.
func itemFiles(root *os.Root, id string) (files, links []string, bytes int64) {
	dir, err := root.Open(VolContent + "/" + id)
	if err != nil {
		return nil, nil, 0
	}
	names, _ := dir.Readdirnames(-1)
	dir.Close()
	sort.Strings(names)
	for _, name := range names {
		info, err := root.Lstat(VolContent + "/" + id + "/" + name)
		if err != nil {
			continue
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			links = append(links, name)
		case info.Mode().IsRegular():
			files = append(files, name)
			if name != PublishData {
				bytes += info.Size()
			}
		}
	}
	return files, links, bytes
}

// link points every shared file of an item into the mount, and drops the
// small descriptor in as a real file.
func (s Store) link(root *os.Root, it Item, owner core.Owner) error {
	dir := VolContent + "/" + it.ID
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	root.Lchown(dir, owner.UID, owner.GID)
	for _, f := range it.Shared() {
		rel := dir + "/" + f
		want := s.LinkTarget(it.ID, it.Manifest, f)
		if cur, err := root.Readlink(rel); err == nil && cur == want {
			continue
		}
		root.Remove(rel)
		if err := root.Symlink(want, rel); err != nil {
			return err
		}
		root.Lchown(rel, owner.UID, owner.GID)
	}
	// stale links from an older version of the same item have no business here
	_, links, _ := itemFiles(root, it.ID)
	keep := map[string]bool{}
	for _, f := range it.Shared() {
		keep[f] = true
	}
	for _, f := range links {
		if !keep[f] {
			root.Remove(dir + "/" + f)
		}
	}
	src := filepath.Join(s.versionDir(it.ID, it.Manifest), PublishData)
	if _, err := root.Stat(dir + "/" + PublishData); err != nil {
		if data, err := os.ReadFile(src); err == nil {
			root.WriteFile(dir+"/"+PublishData, data, 0o644)
			root.Chown(dir+"/"+PublishData, owner.UID, owner.GID)
		}
	}
	return nil
}

// release puts real copies back so the engine can overwrite them.
func (s Store) release(root *os.Root, it Item, owner core.Owner) error {
	for _, f := range it.Shared() {
		rel := VolContent + "/" + it.ID + "/" + f
		in, err := os.Open(filepath.Join(s.versionDir(it.ID, it.Manifest), f))
		if err != nil {
			return err
		}
		root.Remove(rel)
		out, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		in.Close()
		out.Close()
		if err != nil {
			root.Remove(rel)
			return err
		}
		root.Chown(rel, owner.UID, owner.GID)
	}
	return nil
}

// Prune drops stored versions nothing points at any more, older than
// keepDays. used holds "<id>/<manifest>" keys volumes still link to.
func (s Store) Prune(used map[string]bool, keepDays int, now time.Time) (removed int, freed int64) {
	for _, it := range s.Items() {
		if used[it.ID+"/"+it.Manifest] {
			continue
		}
		dir := s.versionDir(it.ID, it.Manifest)
		if keepDays > 0 {
			st, err := os.Stat(dir)
			if err != nil || now.Sub(st.ModTime()) < time.Duration(keepDays)*24*time.Hour {
				continue
			}
		}
		if os.RemoveAll(dir) == nil {
			removed++
			freed += it.Bytes
			os.Remove(filepath.Dir(dir)) // the item dir, when it went empty
		}
	}
	return removed, freed
}

// Used lists what a volume links to, as "<id>/<manifest>" keys.
func Used(store Store, volume string) map[string]bool {
	out := map[string]bool{}
	root, err := os.OpenRoot(volume)
	if err != nil {
		return out
	}
	defer root.Close()
	for _, id := range itemIDs(root) {
		_, links, _ := itemFiles(root, id)
		for _, f := range links {
			target, err := root.Readlink(VolContent + "/" + id + "/" + f)
			if err != nil {
				continue
			}
			if m := manifestOf(target); m != "" {
				out[id+"/"+m] = true
				break
			}
		}
	}
	return out
}

// manifestOf reads the version out of a link target.
func manifestOf(target string) string {
	parts := strings.Split(strings.TrimSuffix(target, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2]
}

// MaxACF bounds the manifest file. It lives in a volume the server's users
// control, and a root daemon that reads whatever size it is handed can be
// OOM-killed by writing a large enough file. Steam's own is a few hundred
// kilobytes at worst.
const MaxACF = 16 << 20

func readACF(root *os.Root) (*KV, error) {
	data, err := readCapped(root, VolACF, MaxACF)
	if err != nil {
		if os.IsNotExist(err) {
			return NewKV(), nil
		}
		return nil, err
	}
	kv, err := ParseACF(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", VolACF, err)
	}
	return kv, nil
}

func writeACF(root *os.Root, kv *KV, owner core.Owner) error {
	if err := root.MkdirAll(VolWorkshop, 0o755); err != nil {
		return err
	}
	tmp := VolACF + ".cs2node"
	root.Remove(tmp)
	if err := root.WriteFile(tmp, []byte(kv.String()), 0o644); err != nil {
		return err
	}
	root.Chown(tmp, owner.UID, owner.GID)
	return root.Rename(tmp, VolACF)
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// readCapped reads a file from a volume, refusing one that is larger than
// max rather than pulling it all into the daemon's memory.
func readCapped(root *os.Root, name string, max int64) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > max {
		return nil, fmt.Errorf("%s is %d bytes, over the %d byte limit", name, st.Size(), max)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is over the %d byte limit", name, max)
	}
	return data, nil
}

// copyOut copies one file out of a volume into the store. The source is
// opened through the volume's root, so a symlink pointing anywhere outside
// it is refused rather than followed.
func copyOut(root *os.Root, rel, dst string) error {
	in, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", rel)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
