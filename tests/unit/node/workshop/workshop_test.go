// SPDX-License-Identifier: GPL-3.0-or-later

package workshop_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/workshop"
)

// realACF is what CS2 writes, tabs and all.
const realACF = `"AppWorkshop"
{
	"appid"		"730"
	"SizeOnDisk"		"83763796"
	"NeedsUpdate"		"0"
	"WorkshopItemsInstalled"
	{
		"3070290869"
		{
			"size"		"83763796"
			"timeupdated"		"1699419831"
			"manifest"		"4513804927223833942"
		}
	}
	"WorkshopItemDetails"
	{
		"3070290869"
		{
			"manifest"		"4513804927223833942"
			"timeupdated"		"1699419831"
			"timetouched"		"1789119575"
			"latest_timeupdated"		"1699419831"
			"latest_manifest"		"4513804927223833942"
		}
	}
}
`

func TestACFRoundTrip(t *testing.T) {
	kv, err := workshop.ParseACF(realACF)
	if err != nil {
		t.Fatal(err)
	}
	app := kv.Sub("AppWorkshop")
	if app == nil || app.Str("appid") != "730" {
		t.Fatalf("root: %v", kv.Keys())
	}
	item := app.Sub("WorkshopItemsInstalled").Sub("3070290869")
	if item.Str("manifest") != "4513804927223833942" || item.Str("size") != "83763796" {
		t.Fatalf("item: %v", item)
	}
	// a second item, then read it back through a full render and parse
	added := app.Sub("WorkshopItemsInstalled").Ensure("3129499780")
	added.SetStr("size", "66636018")
	added.SetStr("manifest", "3592348942011654287")
	again, err := workshop.ParseACF(kv.String())
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, kv.String())
	}
	back := again.Sub("AppWorkshop").Sub("WorkshopItemsInstalled")
	if back.Sub("3129499780").Str("manifest") != "3592348942011654287" {
		t.Fatalf("added item lost: %s", kv.String())
	}
	if back.Sub("3070290869").Str("manifest") != "4513804927223833942" {
		t.Fatal("existing item lost")
	}
	if keys := back.Keys(); len(keys) != 2 || keys[0] != "3070290869" {
		t.Fatalf("order not kept: %v", keys)
	}
	if _, err := workshop.ParseACF(`"broken" { "x" `); err == nil {
		t.Fatal("an unterminated block must fail")
	}
}

// volume builds a server volume holding one downloaded workshop item.
func volume(t *testing.T, acf string) string {
	t.Helper()
	dir := t.TempDir()
	content := filepath.Join(dir, workshop.VolContent, "3070290869")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatal(err)
	}
	// a model pack ships as a dir vpk plus numbered chunks, not one file
	for _, f := range []string{"3070290869_dir.vpk", "3070290869_000.vpk"} {
		if err := os.WriteFile(filepath.Join(content, f), []byte(strings.Repeat("m", 2048)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(content, "publish_data.txt"), []byte("x"), 0o644)
	if acf != "" {
		if err := os.WriteFile(filepath.Join(dir, workshop.VolACF), []byte(acf), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func me() core.Owner { return core.Owner{UID: os.Getuid(), GID: os.Getgid()} }

const (
	v1 = "4513804927223833942"
	v2 = "8888888888888888888"
)

func steamHasV2() map[string]workshop.Detail {
	return map[string]workshop.Detail{"3070290869": {Manifest: v2, Updated: 1799999999}}
}

func TestAbsorbThenShareWithASecondServer(t *testing.T) {
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	first := volume(t, realACF)

	rep, err := store.Sync(first, me(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Absorbed) != 1 || rep.Freed != 4096 || len(rep.Absorbed[0].Files) != 3 {
		t.Fatalf("first pass: %+v", rep)
	}
	// every chunk is linked, and the tiny descriptor stays a real file
	for _, f := range []string{"3070290869_dir.vpk", "3070290869_000.vpk"} {
		target, err := os.Readlink(filepath.Join(first, workshop.VolContent, "3070290869", f))
		if err != nil {
			t.Fatalf("%s must be a link now: %v", f, err)
		}
		if target != "/tmp/cs2-workshop/730/3070290869/4513804927223833942/"+f {
			t.Fatalf("link target: %s", target)
		}
		if _, err := os.Stat(filepath.Join(store.Dir, "730/3070290869/4513804927223833942", f)); err != nil {
			t.Fatalf("store: %v", err)
		}
	}
	pd, err := os.Lstat(filepath.Join(first, workshop.VolContent, "3070290869", "publish_data.txt"))
	if err != nil || !pd.Mode().IsRegular() {
		t.Fatalf("publish_data.txt must stay a real file: %v", err)
	}
	// the map another server downloaded costs the next one nothing
	second := t.TempDir()
	rep2, err := store.Sync(second, me(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep2.Seeded) != 1 {
		t.Fatalf("second server should get it for free: %+v", rep2)
	}
	// both servers must end up on the very same bytes, not two copies
	for _, f := range []string{"3070290869_dir.vpk", "3070290869_000.vpk"} {
		a, err := os.Readlink(filepath.Join(first, workshop.VolContent, "3070290869", f))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.Readlink(filepath.Join(second, workshop.VolContent, "3070290869", f))
		if err != nil {
			t.Fatalf("the second server must link %s too: %v", f, err)
		}
		if a != b {
			t.Fatalf("%s: the two servers point at different files: %q vs %q", f, a, b)
		}
	}
	if n := len(workshop.Store{Dir: store.Dir, Mount: store.Mount}.Items()); n != 1 {
		t.Fatalf("one version stored, not %d", n)
	}
	acf, err := os.ReadFile(filepath.Join(second, workshop.VolACF))
	if err != nil {
		t.Fatal(err)
	}
	kv, err := workshop.ParseACF(string(acf))
	if err != nil {
		t.Fatalf("the seeded acf must parse: %v\n%s", err, acf)
	}
	app := kv.Sub("AppWorkshop")
	seeded := app.Sub("WorkshopItemsInstalled").Sub("3070290869")
	if seeded.Str("manifest") != "4513804927223833942" || seeded.Str("size") != "83763796" {
		t.Fatalf("seeded entry: %s", acf)
	}
	det := app.Sub("WorkshopItemDetails").Sub("3070290869")
	if det.Str("latest_manifest") != det.Str("manifest") || det.Str("timetouched") == "" {
		t.Fatalf("details must say the item is current: %s", acf)
	}
	if app.Str("SizeOnDisk") != "83763796" {
		t.Fatalf("SizeOnDisk: %q", app.Str("SizeOnDisk"))
	}

	// running it again changes nothing
	rep3, err := store.Sync(first, me(), true, nil)
	if err != nil || len(rep3.Absorbed) != 0 || rep3.Freed != 0 {
		t.Fatalf("second pass must be a no-op: %+v %v", rep3, err)
	}
}

func TestSeedOffLeavesOtherServersAlone(t *testing.T) {
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	if _, err := store.Sync(volume(t, realACF), me(), false, nil); err != nil {
		t.Fatal(err)
	}
	second := t.TempDir()
	rep, err := store.Sync(second, me(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Seeded) != 0 {
		t.Fatalf("seeding off: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(second, workshop.VolContent)); !os.IsNotExist(err) {
		t.Fatal("nothing should have been created")
	}
}

// An outdated item has to come back as not installed at all. Real files
// with the old acf entry look installed to MultiAddonManager and
// AddonsManager, which then never fetch the update (issue #8).
func TestAStaleItemIsDroppedSoTheServerDownloadsIt(t *testing.T) {
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	vol := volume(t, realACF)
	if _, err := store.Sync(vol, me(), true, nil); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(vol, workshop.VolContent, "3070290869", "3070290869_dir.vpk")
	if _, err := os.Readlink(link); err != nil {
		t.Fatal("expected a link after the first pass")
	}
	rep, err := store.Sync(vol, me(), true, steamHasV2())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Released) != 1 || len(rep.Linked) != 0 || len(rep.Seeded) != 0 {
		t.Fatalf("stale item not dropped: %+v", rep)
	}
	assertNotInstalled(t, vol)
}

// A server can also hold the outdated version as real files, from an older
// node that handed it back that way. It goes the same way, not into a link.
func TestAServerHoldingTheStaleVersionAsFilesIsDropped(t *testing.T) {
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	if _, err := store.Sync(volume(t, realACF), me(), true, nil); err != nil {
		t.Fatal(err)
	}
	vol := volume(t, realACF)
	rep, err := store.Sync(vol, me(), true, steamHasV2())
	if err != nil || len(rep.Released) != 1 || len(rep.Linked) != 0 || rep.Freed != 0 {
		t.Fatalf("real files of the stale version must go: %+v %v", rep, err)
	}
	assertNotInstalled(t, vol)
}

func TestAFailedDropStillForgetsTheItem(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	vol := volume(t, realACF)
	if _, err := store.Sync(vol, me(), true, nil); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(vol, workshop.VolContent, "3070290869")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if _, err := store.Sync(vol, me(), true, steamHasV2()); err == nil {
		t.Fatal("the failed removal must be reported")
	}
	if app := readVolumeACF(t, vol).Sub("AppWorkshop"); app.Sub("WorkshopItemsInstalled").Sub("3070290869") != nil {
		t.Fatalf("the acf must forget the item anyway: %s", app)
	}
}

func TestAnOlderCopyFollowsTheStoredUpdate(t *testing.T) {
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	a := volume(t, realACF)
	writeManifest(t, a, v2, v2)
	if _, err := store.Sync(a, me(), true, nil); err != nil {
		t.Fatal(err)
	}
	b := volume(t, realACF)
	rep, err := store.Sync(b, me(), true, steamHasV2())
	if err != nil || len(rep.Absorbed) != 0 || len(rep.Linked) != 1 || rep.Linked[0].Manifest != v2 || rep.Freed != 4096 {
		t.Fatalf("must follow v2: %+v %v", rep, err)
	}
	target, err := os.Readlink(filepath.Join(b, workshop.VolContent, "3070290869", "3070290869_dir.vpk"))
	if err != nil || !strings.Contains(target, v2) {
		t.Fatalf("link: %q %v", target, err)
	}
	if d := readVolumeACF(t, b).Sub("AppWorkshop").Sub("WorkshopItemDetails").Sub("3070290869"); d.Str("manifest") != v2 {
		t.Fatalf("acf still on v1: %s", d)
	}
	if n := len(store.Items()); n != 1 {
		t.Fatalf("v1 must not be taken in: %d versions", n)
	}
}

func TestAnOlderCopyIsDroppedWhenTheStoreIsBehindToo(t *testing.T) {
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	a := volume(t, realACF)
	writeManifest(t, a, v2, v2)
	if _, err := store.Sync(a, me(), true, nil); err != nil {
		t.Fatal(err)
	}
	b := volume(t, realACF)
	v3 := map[string]workshop.Detail{"3070290869": {Manifest: "9999999999999999999", Updated: 1899999999}}
	rep, err := store.Sync(b, me(), true, v3)
	if err != nil || len(rep.Released) != 1 || len(rep.Absorbed) != 0 || rep.Freed != 0 {
		t.Fatalf("v1 must go: %+v %v", rep, err)
	}
	assertNotInstalled(t, b)
	if n := len(store.Items()); n != 1 {
		t.Fatalf("v1 must not be taken in: %d versions", n)
	}
}

func TestAFailedTakeKeepsTheServerCopy(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads anything")
	}
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	vol := volume(t, realACF)
	dir := filepath.Join(vol, workshop.VolContent, "3070290869")
	locked := filepath.Join(dir, "3070290869_dir.vpk") // copied after _000
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Sync(vol, me(), true, nil); err == nil {
		t.Fatal("the failed copy must be reported")
	}
	for _, f := range []string{"3070290869_000.vpk", "3070290869_dir.vpk"} {
		if st, err := os.Lstat(filepath.Join(dir, f)); err != nil || !st.Mode().IsRegular() {
			t.Fatalf("%s must stay a real file: %v", f, err)
		}
	}
	if left, _ := os.ReadDir(store.Dir); len(store.Items()) != 0 || len(left) > 1 {
		t.Fatalf("no half version may stay: %v %v", store.Items(), left)
	}
	os.Chmod(locked, 0o644)
	if rep, err := store.Sync(vol, me(), true, nil); err != nil || len(rep.Absorbed) != 1 {
		t.Fatalf("next boot takes it: %+v %v", rep, err)
	}
}

// steam check failing for a while gives an old answer, not an update
func TestAnOlderSteamAnswerKeepsTheNewerCopy(t *testing.T) {
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	vol := volume(t, realACF)
	writeManifest(t, vol, v2, v2)
	if _, err := store.Sync(vol, me(), true, nil); err != nil {
		t.Fatal(err)
	}
	old := map[string]workshop.Detail{"3070290869": {Manifest: v1, Updated: 1699419831}}
	if s := store.Stale(old); len(s) != 0 {
		t.Fatalf("not stale: %v", s)
	}
	rep, err := store.Sync(vol, me(), true, old)
	if err != nil || len(rep.Released) != 0 {
		t.Fatalf("v2 must stay: %+v %v", rep, err)
	}
	if _, err := os.Readlink(filepath.Join(vol, workshop.VolContent, "3070290869", "3070290869_dir.vpk")); err != nil {
		t.Fatalf("v2 must stay linked: %v", err)
	}
}

// assertNotInstalled: no files, no links, no acf entry for the test item.
func assertNotInstalled(t *testing.T, vol string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(vol, workshop.VolContent, "3070290869")); !os.IsNotExist(err) {
		t.Fatalf("the item dir must be gone: %v", err)
	}
	app := readVolumeACF(t, vol).Sub("AppWorkshop")
	if app.Sub("WorkshopItemsInstalled").Sub("3070290869") != nil || app.Sub("WorkshopItemDetails").Sub("3070290869") != nil {
		t.Fatalf("the acf must forget the item: %s", app)
	}
	if app.Str("SizeOnDisk") != "0" {
		t.Fatalf("SizeOnDisk: %q", app.Str("SizeOnDisk"))
	}
}

func readVolumeACF(t *testing.T, vol string) *workshop.KV {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(vol, workshop.VolACF))
	if err != nil {
		t.Fatal(err)
	}
	kv, err := workshop.ParseACF(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return kv
}

func TestStaleComparesTheNewestStoredVersion(t *testing.T) {
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	if _, err := store.Sync(volume(t, realACF), me(), true, nil); err != nil {
		t.Fatal(err)
	}
	if s := store.Stale(map[string]workshop.Detail{"3070290869": {Manifest: v1, Updated: 1699419831}, "1": {Manifest: "2", Updated: 1}}); len(s) != 0 {
		t.Fatalf("current or unknown items are not stale: %v", s)
	}
	if s := store.Stale(steamHasV2()); !s["3070290869"] || len(s) != 1 {
		t.Fatalf("a newer upstream manifest must mark it stale: %v", s)
	}
}

// `cs2node workshop sync` must ask Steam like the daemon does, or it puts
// the old version straight back.
func TestCLISyncDropsWhatSteamUpdated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":{"publishedfiledetails":[{"publishedfileid":"3070290869","result":1,"hcontent_file":"8888888888888888888","time_updated":1799999999}]}}`))
	}))
	defer srv.Close()
	old := workshop.DetailsURL
	workshop.DetailsURL = srv.URL
	defer func() { workshop.DetailsURL = old }()

	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	vol := volume(t, realACF)
	if _, err := store.Sync(vol, me(), true, nil); err != nil {
		t.Fatal(err)
	}
	tools := &workshop.Tools{Cfg: workshop.Config{Seed: true}, Store: store, Out: io.Discard, HTTP: srv.Client()}
	conts := []core.Container{{Name: "srv", Volume: vol, Owner: me()}}
	if err := tools.Sync(context.Background(), conts, map[string]bool{}, ""); err != nil {
		t.Fatal(err)
	}
	assertNotInstalled(t, vol)
}

// an item the store never had went in unchecked, old version and all
func TestCLISyncAsksSteamAboutNewItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":{"publishedfiledetails":[{"publishedfileid":"3070290869","result":1,"hcontent_file":"8888888888888888888","time_updated":1799999999}]}}`))
	}))
	defer srv.Close()
	old := workshop.DetailsURL
	workshop.DetailsURL = srv.URL
	defer func() { workshop.DetailsURL = old }()

	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	vol := volume(t, realACF)
	tools := &workshop.Tools{Cfg: workshop.Config{Seed: true}, Store: store, Out: io.Discard, HTTP: srv.Client()}
	if err := tools.Sync(context.Background(), []core.Container{{Name: "srv", Volume: vol, Owner: me()}}, map[string]bool{}, ""); err != nil {
		t.Fatal(err)
	}
	assertNotInstalled(t, vol)
	if len(store.Items()) != 0 {
		t.Fatal("the outdated version went into the store")
	}
}

func TestLatestSkipsGoneItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":{"publishedfiledetails":[
			{"publishedfileid":"1","result":1,"hcontent_file":"11"},
			{"publishedfileid":"2","result":9},
			{"publishedfileid":"3","result":1,"hcontent_file":""}
		]}}`))
	}))
	defer srv.Close()
	old := workshop.DetailsURL
	workshop.DetailsURL = srv.URL
	defer func() { workshop.DetailsURL = old }()

	got, err := workshop.Latest(context.Background(), srv.Client(), []string{"1", "2", "3"})
	if err != nil || len(got) != 1 || got["1"].Manifest != "11" {
		t.Fatalf("latest: %v %v", got, err)
	}
}

func TestPruneKeepsWhatIsLinked(t *testing.T) {
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	vol := volume(t, realACF)
	if _, err := store.Sync(vol, me(), true, nil); err != nil {
		t.Fatal(err)
	}
	if used := workshop.Used(store, vol); !used["3070290869/4513804927223833942"] {
		t.Fatalf("used: %v", used)
	}
	now := time.Now()
	if n, _ := store.Prune([]core.Container{{Volume: vol}}, 1, now.Add(48*time.Hour)); n != 0 {
		t.Fatal("a linked version must survive")
	}
	// keep_days counts from the last time it was linked, not from when it was stored
	if n, _ := store.Prune(nil, 1, now.Add(60*time.Hour)); n != 0 {
		t.Fatal("unused for half a day, removed with keep_days 1")
	}
	if n, _ := store.Prune(nil, 0, now.Add(1000*24*time.Hour)); n != 0 {
		t.Fatal("keep_days 0 must keep everything")
	}
	if n, freed := store.Prune(nil, 1, now.Add(80*time.Hour)); n != 1 || freed != 4096 {
		t.Fatalf("an unused version should go: %d %d", n, freed)
	}
}

func TestSteamDetailsParsesBothSizeShapes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("publishedfileids[0]") != "3070290869" {
			t.Errorf("request form: %v %v", err, r.PostForm)
		}
		w.Write([]byte(`{"response":{"result":1,"resultcount":2,"publishedfiledetails":[
			{"publishedfileid":"3070290869","result":1,"hcontent_file":"4513804927223833942","time_updated":1699419831,"file_size":"83763796"},
			{"publishedfileid":"3129499780","result":1,"hcontent_file":"999","time_updated":1710010305,"file_size":66636018}
		]}}`))
	}))
	defer srv.Close()
	old := workshop.DetailsURL
	workshop.DetailsURL = srv.URL
	defer func() { workshop.DetailsURL = old }()

	got, err := workshop.Details(context.Background(), srv.Client(), []string{"3070290869", "3129499780"})
	if err != nil {
		t.Fatal(err)
	}
	if got["3070290869"].Manifest != "4513804927223833942" || got["3070290869"].Size != 83763796 {
		t.Fatalf("string size: %+v", got["3070290869"])
	}
	if got["3129499780"].Size != 66636018 || got["3129499780"].Updated != 1710010305 {
		t.Fatalf("number size: %+v", got["3129499780"])
	}
	if empty, err := workshop.Details(context.Background(), srv.Client(), nil); err != nil || len(empty) != 0 {
		t.Fatal("no ids, no call")
	}
}

func TestConfigCheck(t *testing.T) {
	ok := workshop.Defaults()
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []workshop.Config{
		{Dir: "relative", Mount: "/tmp/x", CheckMinutes: 60},
		{Dir: "/srv/x", Mount: "relative", CheckMinutes: 60},
		{Dir: "/srv/x", Mount: "/tmp/x", CheckMinutes: 1},
		{Dir: "/srv/x", Mount: "/tmp/x", CheckMinutes: 60, KeepDays: -1},
		{Dir: "/srv/x", Mount: "/tmp/x", CheckMinutes: 60, PruneHours: -1},
	} {
		if err := bad.Check(); err == nil {
			t.Fatalf("accepted: %+v", bad)
		}
	}
}

// The point of the cache during an update: one server fetches the new
// version, every other one follows by link, nobody downloads twice.
func TestOnlyOneServerDownloadsAnUpdate(t *testing.T) {
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	a, b := volume(t, realACF), volume(t, realACF)
	for _, v := range []string{a, b} {
		if _, err := store.Sync(v, me(), true, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Steam reports a newer version: the first server to boot loses the
	// item, so its own Steam client fetches the current one
	rep, err := store.Sync(a, me(), true, steamHasV2())
	if err != nil || len(rep.Released) != 1 {
		t.Fatalf("drop: %+v %v", rep, err)
	}
	// that server downloads v2 and records it
	content := filepath.Join(a, workshop.VolContent, "3070290869")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"3070290869_dir.vpk", "3070290869_000.vpk"} {
		if err := os.WriteFile(filepath.Join(content, f), []byte(strings.Repeat("n", 3072)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest(t, a, v2, v2) // a real update bumps the published time too

	// next boot: the node takes the new version over
	rep, err = store.Sync(a, me(), true, steamHasV2())
	if err != nil || len(rep.Absorbed) != 1 || rep.Absorbed[0].Manifest != v2 {
		t.Fatalf("absorb of the new version: %+v %v", rep, err)
	}
	if _, ok := store.NewestAll()["3070290869"]; !ok {
		t.Fatal("store lost the item")
	}
	if newest := store.NewestAll()["3070290869"]; newest.Manifest != v2 {
		t.Fatalf("newest should be the update: %s", newest.Manifest)
	}

	// the second server: the store already has v2, so it is no longer
	// stale for anyone; it relinks and downloads nothing
	rep, err = store.Sync(b, me(), true, nil)
	if err != nil || len(rep.Released) != 0 || len(rep.Linked) != 1 {
		t.Fatalf("second server should just relink: %+v %v", rep, err)
	}
	target, err := os.Readlink(filepath.Join(b, workshop.VolContent, "3070290869", "3070290869_dir.vpk"))
	if err != nil || !strings.Contains(target, v2) {
		t.Fatalf("second server must point at the new version: %q %v", target, err)
	}
	// and its bookkeeping must agree, or its Steam client would fetch v2 again
	acf, _ := os.ReadFile(filepath.Join(b, workshop.VolACF))
	kv, err := workshop.ParseACF(string(acf))
	if err != nil {
		t.Fatal(err)
	}
	d := kv.Sub("AppWorkshop").Sub("WorkshopItemDetails").Sub("3070290869")
	if d.Str("manifest") != v2 || d.Str("latest_manifest") != v2 {
		t.Fatalf("acf still on the old version: %s", acf)
	}
	if both := store.Items(); len(both) != 2 {
		t.Fatalf("both versions stay until nothing links to the old one: %d", len(both))
	}
	// once nobody links v1 it goes
	used := map[string]bool{}
	for _, v := range []string{a, b} {
		for k := range workshop.Used(store, v) {
			used[k] = true
		}
	}
	if used["3070290869/"+v1] {
		t.Fatal("nothing should link the old version any more")
	}
	if n, _ := store.Prune([]core.Container{{Volume: a}, {Volume: b}}, 1, time.Now().Add(48*time.Hour)); n != 1 {
		t.Fatalf("the old version should be prunable: %d", n)
	}
}

// writeManifest rewrites the volume's bookkeeping the way the game does
// after a download.
func writeManifest(t *testing.T, volume, manifest, latest string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(volume, workshop.VolACF))
	if err != nil {
		t.Fatal(err)
	}
	kv, err := workshop.ParseACF(string(data))
	if err != nil {
		t.Fatal(err)
	}
	app := kv.Sub("AppWorkshop")
	inst := app.Sub("WorkshopItemsInstalled").Ensure("3070290869")
	inst.SetStr("manifest", manifest)
	inst.SetStr("timeupdated", "1799999999")
	d := app.Sub("WorkshopItemDetails").Ensure("3070290869")
	d.SetStr("manifest", manifest)
	d.SetStr("timeupdated", "1799999999")
	d.SetStr("latest_manifest", latest)
	if err := os.WriteFile(filepath.Join(volume, workshop.VolACF), []byte(kv.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Items published before Source 2 carry no version id. The game records
// "-1" for them, which cannot be compared with anything, so they must stay
// with the server that downloaded them instead of flip-flopping between
// links and files on every boot.
func TestLegacyItemsAreLeftAlone(t *testing.T) {
	if workshop.ValidManifest("4513804927223833942") != true ||
		workshop.ValidManifest("-1") || workshop.ValidManifest("0") ||
		workshop.ValidManifest("") || workshop.ValidManifest("abc") {
		t.Fatal("ValidManifest")
	}
	legacyACF := strings.ReplaceAll(realACF, `"manifest"		"4513804927223833942"`, `"manifest"		"-1"`)
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	vol := volume(t, legacyACF)
	rep, err := store.Sync(vol, me(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Absorbed) != 0 || len(rep.Linked) != 0 {
		t.Fatalf("a legacy item must not be taken over: %+v", rep)
	}
	for _, f := range []string{"3070290869_dir.vpk", "3070290869_000.vpk"} {
		info, err := os.Lstat(filepath.Join(vol, workshop.VolContent, "3070290869", f))
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s must stay a real file: %v", f, err)
		}
	}
	if n := len(store.Items()); n != 0 {
		t.Fatalf("nothing should be stored: %d", n)
	}
}

// The manifest file lives in a volume the server's users control, so its
// size and shape are attacker input to a root daemon.
func TestACFParserRefusesAnAbusiveDocument(t *testing.T) {
	deep := strings.Repeat(`"a"{`, workshop.MaxDepth+10) + strings.Repeat("}", workshop.MaxDepth+10)
	if _, err := workshop.ParseACF(`"AppWorkshop"{` + deep + `}`); err == nil {
		t.Fatal("a deeply nested document must be refused, not recursed into until the stack dies")
	}
	// the shapes Steam actually writes still parse
	kv, err := workshop.ParseACF(`"AppWorkshop" { "WorkshopItemsInstalled" { "123" { "manifest" "456" } } }`)
	if err != nil || kv == nil {
		t.Fatalf("a normal document must still parse: %v", err)
	}
}
