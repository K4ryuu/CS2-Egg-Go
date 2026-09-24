// SPDX-License-Identifier: GPL-3.0-or-later

package workshop_test

import (
	"context"
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

func TestAStaleItemGoesBackAsARealFile(t *testing.T) {
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	vol := volume(t, realACF)
	if _, err := store.Sync(vol, me(), true, nil); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(vol, workshop.VolContent, "3070290869", "3070290869_dir.vpk")
	if _, err := os.Readlink(link); err != nil {
		t.Fatal("expected a link after the first pass")
	}
	// Valve published a new version: the server must be able to write
	rep, err := store.Sync(vol, me(), true, map[string]bool{"3070290869": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Released) != 1 {
		t.Fatalf("stale item not released: %+v", rep)
	}
	for _, f := range []string{"3070290869_dir.vpk", "3070290869_000.vpk"} {
		info, err := os.Lstat(filepath.Join(vol, workshop.VolContent, "3070290869", f))
		if err != nil || !info.Mode().IsRegular() || info.Size() != 2048 {
			t.Fatalf("a released item must be real files again: %s %v %+v", f, err, info)
		}
	}
}

func TestPruneKeepsWhatIsLinked(t *testing.T) {
	store := workshop.Store{Dir: t.TempDir(), Mount: "/tmp/cs2-workshop"}
	vol := volume(t, realACF)
	if _, err := store.Sync(vol, me(), true, nil); err != nil {
		t.Fatal(err)
	}
	used := workshop.Used(store, vol)
	if !used["3070290869/4513804927223833942"] {
		t.Fatalf("used: %v", used)
	}
	if n, _ := store.Prune(used, 0, time.Now()); n != 0 {
		t.Fatal("a linked version must survive")
	}
	if n, freed := store.Prune(map[string]bool{}, 0, time.Now()); n != 1 || freed != 4096 {
		t.Fatalf("an unused version should go: %d %d", n, freed)
	}
	if n, _ := store.Prune(map[string]bool{}, 30, time.Now()); n != 0 {
		t.Fatal("nothing left to remove")
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
	const v1 = "4513804927223833942"
	const v2 = "8888888888888888888"

	// Steam reports a newer version: the first server to boot gets its
	// files back so its own Steam client can update them
	rep, err := store.Sync(a, me(), true, map[string]bool{"3070290869": true})
	if err != nil || len(rep.Released) != 1 {
		t.Fatalf("release: %+v %v", rep, err)
	}
	// that server downloads v2 over the released files and records it
	for _, f := range []string{"3070290869_dir.vpk", "3070290869_000.vpk"} {
		if err := os.WriteFile(filepath.Join(a, workshop.VolContent, "3070290869", f), []byte(strings.Repeat("n", 3072)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest(t, a, v2, v2) // a real update bumps the published time too

	// next boot: the node takes the new version over
	rep, err = store.Sync(a, me(), true, map[string]bool{"3070290869": true})
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
	if n, _ := store.Prune(used, 0, time.Now()); n != 1 {
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
