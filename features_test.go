package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- tags -------------------------------------------------------------------

func TestTagsAreStoredNormalisedAndCaseFolded(t *testing.T) {
	app := newTestApp(t)
	id := seed(t, app.store, Item{Name: "ESP32", Tags: []string{"WiFi", "3v3"}})[0]

	// A second item reusing the same tag in different case must not create a
	// duplicate row, or the tag filter shows the same tag twice.
	seed(t, app.store, Item{Name: "ESP8266", Tags: []string{"wifi"}})

	facets, err := app.store.TagFacets()
	if err != nil {
		t.Fatalf("TagFacets: %v", err)
	}
	counts := map[string]int{}
	for _, f := range facets {
		counts[strings.ToLower(f.Value)] = f.Count
	}
	if len(facets) != 2 {
		t.Errorf("tag facets = %+v, want 2 distinct tags", facets)
	}
	if counts["wifi"] != 2 {
		t.Errorf("wifi tag count = %d, want 2 (case-insensitive)", counts["wifi"])
	}

	it, _ := app.store.GetItem(id)
	if got := it.TagString(); got != "3v3, WiFi" {
		t.Errorf("tags = %q, want them sorted and preserved as first written", got)
	}
}

func TestFilteringBySeveralTagsNarrows(t *testing.T) {
	app := newTestApp(t)
	seed(t, app.store,
		Item{Name: "SMD resistor", Tags: []string{"smd", "passive"}},
		Item{Name: "SMD LED", Tags: []string{"smd", "optical"}},
		Item{Name: "Through-hole resistor", Tags: []string{"tht", "passive"}},
	)

	one, _ := app.store.ListItems(Query{Tags: []string{"smd"}})
	if len(one) != 2 {
		t.Errorf("one tag matched %d items, want 2", len(one))
	}
	both, _ := app.store.ListItems(Query{Tags: []string{"smd", "passive"}})
	if len(both) != 1 || both[0].Name != "SMD resistor" {
		t.Errorf("two tags matched %d items, want only the SMD resistor", len(both))
	}
	none, _ := app.store.ListItems(Query{Tags: []string{"smd", "tht"}})
	if len(none) != 0 {
		t.Errorf("contradictory tags matched %d items, want 0", len(none))
	}
}

func TestUpdatingTagsRemovesOrphans(t *testing.T) {
	app := newTestApp(t)
	id := seed(t, app.store, Item{Name: "Board", Tags: []string{"temporary"}})[0]

	it, _ := app.store.GetItem(id)
	it.Tags = []string{"kept"}
	if err := app.store.UpdateItem(it); err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}
	facets, _ := app.store.TagFacets()
	if len(facets) != 1 || facets[0].Value != "kept" {
		t.Errorf("tag facets = %+v, want only \"kept\" -- unused tags should be dropped", facets)
	}
}

// --- folders ----------------------------------------------------------------

func TestFolderTreeRollsUpDescendantCounts(t *testing.T) {
	app := newTestApp(t)
	lab, _ := app.store.CreateFolder("Lab", nil)
	bench, _ := app.store.CreateFolder("Bench", &lab)
	drawer, _ := app.store.CreateFolder("Drawer", &bench)
	other, _ := app.store.CreateFolder("Storage", nil)

	seed(t, app.store,
		Item{Name: "Scope", Quantity: 1, FolderID: &lab},
		Item{Name: "Iron", Quantity: 2, FolderID: &bench},
		Item{Name: "Resistors", Quantity: 500, FolderID: &drawer},
		Item{Name: "Boxes", Quantity: 4, FolderID: &other},
		Item{Name: "Loose part", Quantity: 9},
	)

	tree, err := app.store.FolderTree()
	if err != nil {
		t.Fatalf("FolderTree: %v", err)
	}
	if len(tree) != 2 {
		t.Fatalf("got %d root folders, want 2", len(tree))
	}

	byName := map[string]*Folder{}
	for _, f := range FlattenFolders(tree) {
		byName[f.Name] = f
	}
	// Lab holds one item directly but three counting Bench and Drawer.
	if got := byName["Lab"].ItemCount; got != 1 {
		t.Errorf("Lab direct items = %d, want 1", got)
	}
	if got := byName["Lab"].TotalItems; got != 3 {
		t.Errorf("Lab total items = %d, want 3 including subfolders", got)
	}
	if got := byName["Lab"].TotalPieces; got != 503 {
		t.Errorf("Lab total pieces = %d, want 503", got)
	}
	if got := byName["Drawer"].Depth; got != 2 {
		t.Errorf("Drawer depth = %d, want 2", got)
	}
}

func TestFolderScopedListing(t *testing.T) {
	app := newTestApp(t)
	parent, _ := app.store.CreateFolder("Parent", nil)
	child, _ := app.store.CreateFolder("Child", &parent)

	seed(t, app.store,
		Item{Name: "In parent", FolderID: &parent},
		Item{Name: "In child", FolderID: &child},
		Item{Name: "Unfiled thing"},
	)

	direct, _ := app.store.ListItems(Query{FolderID: &parent})
	if len(direct) != 1 || direct[0].Name != "In parent" {
		t.Errorf("folder listing = %d items, want only the folder's own", len(direct))
	}
	deep, _ := app.store.ListItems(Query{FolderID: &parent, Recursive: true})
	if len(deep) != 2 {
		t.Errorf("recursive folder listing = %d items, want 2", len(deep))
	}
	unfiled, _ := app.store.ListItems(Query{Unfiled: true})
	if len(unfiled) != 1 || unfiled[0].Name != "Unfiled thing" {
		t.Errorf("unfiled listing = %+v, want just the unfiled item", unfiled)
	}
}

func TestDeletingFolderKeepsItemsAndUnfilesThem(t *testing.T) {
	app := newTestApp(t)
	parent, _ := app.store.CreateFolder("Parent", nil)
	child, _ := app.store.CreateFolder("Child", &parent)
	ids := seed(t, app.store,
		Item{Name: "Top item", FolderID: &parent},
		Item{Name: "Nested item", FolderID: &child},
	)

	if err := app.store.DeleteFolder(parent); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	for _, id := range ids {
		it, err := app.store.GetItem(id)
		if err != nil {
			t.Fatalf("item %d was deleted along with its folder: %v", id, err)
		}
		if it.FolderID != nil {
			t.Errorf("%s still points at folder %d after the folder was deleted", it.Name, *it.FolderID)
		}
	}
	if _, err := app.store.GetFolder(child); err != sql.ErrNoRows {
		t.Error("subfolder outlived its parent")
	}
}

func TestFolderCannotBeMovedIntoItsOwnSubtree(t *testing.T) {
	app := newTestApp(t)
	root, _ := app.store.CreateFolder("Root", nil)
	mid, _ := app.store.CreateFolder("Mid", &root)
	leaf, _ := app.store.CreateFolder("Leaf", &mid)

	if err := app.store.RenameFolder(root, "Root", &leaf); err == nil {
		t.Error("moving a folder under its own descendant was allowed, which would orphan the branch")
	}
	if err := app.store.RenameFolder(root, "Root", &root); err == nil {
		t.Error("a folder was allowed to become its own parent")
	}
	// A legitimate move still works.
	if err := app.store.RenameFolder(leaf, "Leaf", &root); err != nil {
		t.Errorf("valid reparent rejected: %v", err)
	}
}

func TestAncestorsGivesBreadcrumbPath(t *testing.T) {
	app := newTestApp(t)
	a, _ := app.store.CreateFolder("A", nil)
	b, _ := app.store.CreateFolder("B", &a)
	c, _ := app.store.CreateFolder("C", &b)

	path, err := app.store.Ancestors(c)
	if err != nil {
		t.Fatalf("Ancestors: %v", err)
	}
	var names []string
	for _, f := range path {
		names = append(names, f.Name)
	}
	if strings.Join(names, "/") != "A/B/C" {
		t.Errorf("breadcrumb = %v, want A/B/C (root first)", names)
	}
}

// --- SSRF guards ------------------------------------------------------------

func TestPrivateAddressesAreRejected(t *testing.T) {
	blocked := []string{
		"127.0.0.1",        // loopback
		"10.1.2.3",         // RFC1918
		"192.168.1.1",      // home router
		"172.16.0.1",       // RFC1918
		"169.254.169.254",  // cloud metadata
		"100.64.0.1",       // carrier NAT / Tailscale
		"0.0.0.0",          // unspecified
		"::1",              // IPv6 loopback
		"fd00::1",          // IPv6 unique-local
		"fe80::1",          // IPv6 link-local
		"::ffff:127.0.0.1", // IPv4-mapped loopback
		"255.255.255.255",  // broadcast
	}
	for _, s := range blocked {
		ip := netip.MustParseAddr(s)
		if isPublicIP(ip) {
			t.Errorf("isPublicIP(%s) = true, want false -- this address is reachable only from inside the network", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "93.184.216.34", "2606:4700::1111"} {
		if !isPublicIP(netip.MustParseAddr(s)) {
			t.Errorf("isPublicIP(%s) = false, want true", s)
		}
	}
}

func TestFetcherRefusesPrivateHostsByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>Router admin</title></head></html>"))
	}))
	defer srv.Close()

	// httptest listens on loopback, which is exactly what must be blocked.
	guarded := NewFetcher(false)
	if _, err := guarded.FetchPage(context.Background(), srv.URL); err == nil {
		t.Fatal("fetcher reached a loopback address with the guard on")
	} else if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("error = %v, want it to explain the address was refused", err)
	}

	// And the escape hatch works, otherwise nobody could use a LAN wiki.
	open := NewFetcher(true)
	meta, err := open.FetchPage(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("ALLOW_PRIVATE_FETCH did not permit a LAN fetch: %v", err)
	}
	if meta.Title != "Router admin" {
		t.Errorf("title = %q, want %q", meta.Title, "Router admin")
	}
}

func TestFetcherRejectsNonHTTPSchemes(t *testing.T) {
	f := NewFetcher(true)
	for _, raw := range []string{"file:///etc/passwd", "gopher://x/1", "ftp://example.com/f"} {
		if _, err := f.FetchPage(context.Background(), raw); err == nil {
			t.Errorf("FetchPage(%q) succeeded, want a scheme rejection", raw)
		}
	}
}

// --- metadata import --------------------------------------------------------

const productPage = `<!doctype html><html><head>
<title>Ignored when OpenGraph is present</title>
<meta property="og:title" content="ESP32-WROOM-32 Development Board">
<meta property="og:description" content="  38-pin devkit   with   USB-C. ">
<meta property="og:site_name" content="Example Parts">
<meta property="og:image" content="/img/board.jpg">
<meta property="product:price:amount" content="7.50">
<meta property="product:price:currency" content="USD">
<meta property="mpn" content="ESP32-WROOM-32E">
</head><body>irrelevant</body></html>`

func TestFetchPageReadsOpenGraphMetadata(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(productPage))
	}))
	defer srv.Close()

	meta, err := NewFetcher(true).FetchPage(context.Background(), srv.URL+"/products/esp32")
	if err != nil {
		t.Fatalf("FetchPage: %v", err)
	}
	if meta.Title != "ESP32-WROOM-32 Development Board" {
		t.Errorf("title = %q", meta.Title)
	}
	if meta.Description != "38-pin devkit with USB-C." {
		t.Errorf("description = %q, want whitespace collapsed", meta.Description)
	}
	if meta.Value != "USD 7.50" {
		t.Errorf("value = %q, want the price with its currency", meta.Value)
	}
	if meta.PartNumber != "ESP32-WROOM-32E" {
		t.Errorf("part number = %q", meta.PartNumber)
	}
	// A root-relative og:image has to be resolved against the page URL or the
	// follow-up image fetch has nothing to work with.
	want := srv.URL + "/img/board.jpg"
	if meta.ImageURL != want {
		t.Errorf("image = %q, want it resolved to %q", meta.ImageURL, want)
	}
	if meta.SiteName != "Example Parts" {
		t.Errorf("site = %q", meta.SiteName)
	}
}

func TestFetchPageFallsBackToTitleTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><title>  Plain   Datasheet  </title></head></html>`))
	}))
	defer srv.Close()

	meta, err := NewFetcher(true).FetchPage(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchPage: %v", err)
	}
	if meta.Title != "Plain Datasheet" {
		t.Errorf("title = %q, want the <title> used when OpenGraph is missing", meta.Title)
	}
}

func TestImportPreviewEndpointReturnsFields(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(productPage))
	}))
	defer page.Close()

	app := newTestApp(t)
	srv := httptest.NewServer(app.routes())
	defer srv.Close()

	res, err := http.PostForm(srv.URL+"/import/preview", url.Values{"url": {page.URL}})
	if err != nil {
		t.Fatalf("POST /import/preview: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	body := readAll(t, res.Body)
	for _, want := range []string{"ESP32-WROOM-32 Development Board", "USD 7.50", "ESP32-WROOM-32E", "/img/board.jpg"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview response missing %q\ngot: %s", want, body)
		}
	}
}

func TestImportPreviewReportsBadURLAsClientError(t *testing.T) {
	app := newTestApp(t)
	srv := httptest.NewServer(app.routes())
	defer srv.Close()

	res, err := http.PostForm(srv.URL+"/import/preview", url.Values{"url": {"not a url at all"}})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 -- a bad paste is the user's error, not a server fault", res.StatusCode)
	}
}

func TestPhotoImportFromURL(t *testing.T) {
	jpegBytes := testJPEG(t, 800, 600, 0)
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".jpg") {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(jpegBytes)
			return
		}
		// A product page, so the fallback path gets exercised too.
		fmt.Fprintf(w, `<html><head><meta property="og:image" content="/board.jpg"></head></html>`)
	}))
	defer imgSrv.Close()

	app := newTestApp(t)
	id := seed(t, app.store, Item{Name: "Board"})[0]
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	// Direct image URL.
	if msg := app.savePhotoFromURL(r, id, imgSrv.URL+"/board.jpg"); msg != "" {
		t.Fatalf("direct image import failed: %s", msg)
	}
	// A page URL: the image should be found via its og:image.
	if msg := app.savePhotoFromURL(r, id, imgSrv.URL+"/product"); msg != "" {
		t.Fatalf("page import did not fall back to og:image: %s", msg)
	}

	it, _ := app.store.GetItem(id)
	if len(it.Photos) != 2 {
		t.Fatalf("item has %d photos, want 2", len(it.Photos))
	}
	for _, p := range it.Photos {
		if !safeName.MatchString(p.Filename) {
			t.Errorf("stored filename %q is not servable", p.Filename)
		}
		if _, _, err := decodeFile(app.photos.ThumbPath(p.Filename)); err != nil {
			t.Errorf("no thumbnail generated for imported photo: %v", err)
		}
	}
}

func TestPhotoImportRejectsNonImagePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>No pictures here</title></head></html>"))
	}))
	defer srv.Close()

	app := newTestApp(t)
	id := seed(t, app.store, Item{Name: "Board"})[0]
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	if msg := app.savePhotoFromURL(r, id, srv.URL); msg == "" {
		t.Error("importing a page with no image reported success")
	}
	it, _ := app.store.GetItem(id)
	if len(it.Photos) != 0 {
		t.Errorf("item gained %d photos from a failed import", len(it.Photos))
	}
}

// --- migrations -------------------------------------------------------------

func TestMigrationCarriesOldTagsIntoTheTagTables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")

	// Build a database at the original schema, as an existing deployment has.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := raw.Exec(migrations[0].sql); err != nil {
		t.Fatalf("seed old schema: %v", err)
	}
	_, err = raw.Exec(`INSERT INTO items (name, category, quantity, location, part_number,
		value, tags, link, notes, created_at, updated_at)
		VALUES ('Old item','MCU',3,'Drawer 1','X','v','wifi, SMD , wifi','','',
			'2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seed old row: %v", err)
	}
	raw.Close()

	// Opening it with the current build must upgrade it in place.
	db, err := openDB(path)
	if err != nil {
		t.Fatalf("openDB on an old database: %v", err)
	}
	defer db.Close()
	store := &Store{db: db}

	items, err := store.ListItems(Query{})
	if err != nil {
		t.Fatalf("ListItems after migration: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items after migration, want the existing row preserved", len(items))
	}
	if got := items[0].TagString(); got != "SMD, wifi" {
		t.Errorf("migrated tags = %q, want the old comma list split and de-duplicated", got)
	}
	if items[0].Name != "Old item" || items[0].Quantity != 3 {
		t.Errorf("existing data changed during migration: %+v", items[0])
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("user_version = %d, want %d", version, len(migrations))
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twice.db")
	for i := 0; i < 3; i++ {
		db, err := openDB(path)
		if err != nil {
			t.Fatalf("open %d: %v", i+1, err)
		}
		db.Close()
	}
}

// --- pages ------------------------------------------------------------------

func TestDashboardAndFolderPagesRender(t *testing.T) {
	app := newTestApp(t)
	lab, _ := app.store.CreateFolder("Lab bench", nil)
	seed(t, app.store,
		Item{Name: "Soldering iron", Quantity: 1, FolderID: &lab, Tags: []string{"tools"}},
		Item{Name: "Empty reel", Quantity: 0},
	)

	srv := httptest.NewServer(app.routes())
	defer srv.Close()
	client := &http.Client{}

	dash := getBody(t, client, srv.URL+"/")
	for _, want := range []string{"Lab bench", "Backoffice", "tools", "Running low", "Unfiled"} {
		if !strings.Contains(dash, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}

	folder := getBody(t, client, srv.URL+fmt.Sprintf("/folders/%d", lab))
	if !strings.Contains(folder, "Soldering iron") {
		t.Error("folder page does not list its item")
	}
	if strings.Contains(folder, "Empty reel") {
		t.Error("folder page leaked an item from outside the folder")
	}

	// Tag filtering from the grid.
	tagged := getBody(t, client, srv.URL+"/items?tag=tools")
	if !strings.Contains(tagged, "Soldering iron") || strings.Contains(tagged, "Empty reel") {
		t.Error("tag filter did not narrow the grid")
	}
}

func TestCreateItemWithFolderAndTagsOverHTTP(t *testing.T) {
	app := newTestApp(t)
	folder, _ := app.store.CreateFolder("Sensors", nil)

	srv := httptest.NewServer(app.routes())
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	form := url.Values{
		"name":      {"DS18B20"},
		"quantity":  {"9"},
		"folder_id": {fmt.Sprint(folder)},
		"tags":      {"1-wire, temperature, 1-wire"},
	}
	res, err := client.PostForm(srv.URL+"/items", form)
	if err != nil {
		t.Fatalf("POST /items: %v", err)
	}
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", res.StatusCode)
	}

	items, _ := app.store.ListItems(Query{FolderID: &folder})
	if len(items) != 1 {
		t.Fatalf("folder holds %d items, want 1", len(items))
	}
	if got := items[0].TagString(); got != "1-wire, temperature" {
		t.Errorf("tags = %q, want duplicates dropped", got)
	}
}

func TestQueryURLTogglesAndPreservesFilters(t *testing.T) {
	q := Query{Search: "esp", Category: "MCU", Tags: []string{"wifi"}}

	got := q.URL("location", "Drawer 3")
	for _, want := range []string{"q=esp", "category=MCU", "tag=wifi", "location=Drawer+3"} {
		if !strings.Contains(got, want) {
			t.Errorf("URL = %q, missing %q -- filters must compose", got, want)
		}
	}
	// Re-selecting the active value clears it.
	if got := q.URL("category", "MCU"); strings.Contains(got, "category=") {
		t.Errorf("URL = %q, want the category cleared when re-selected", got)
	}
	if got := q.WithTagToggled("wifi"); strings.Contains(got, "tag=") {
		t.Errorf("URL = %q, want the tag removed when toggled off", got)
	}
	if got := q.WithTagToggled("smd"); !strings.Contains(got, "tag=wifi") || !strings.Contains(got, "tag=smd") {
		t.Errorf("URL = %q, want both tags kept", got)
	}

	// Inside a folder the filters stay on the folder's own page.
	id := int64(7)
	scoped := Query{FolderID: &id, Sort: "name"}
	if got := scoped.URL("sort", "low"); !strings.HasPrefix(got, "/folders/7?") {
		t.Errorf("URL = %q, want it to stay under /folders/7", got)
	}
}

func TestImportDecodesDoubleEncodedEntities(t *testing.T) {
	// Real shops (Adafruit among them) emit OpenGraph text whose entities are
	// encoded twice, so the parser yields a literal "&#39;" in the description.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head>
			<meta property="og:title" content="Adafruit HUZZAH32 &amp;#8211; ESP32 Feather">
			<meta property="og:description" content="Aww yeah, it&amp;#39;s the Feather you&amp;#39;ve waited for.">
			</head></html>`)
	}))
	defer srv.Close()

	meta, err := NewFetcher(true).FetchPage(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchPage: %v", err)
	}
	if strings.Contains(meta.Title, "&#") || strings.Contains(meta.Description, "&#") {
		t.Errorf("entities survived into the form:\n title = %q\n notes = %q", meta.Title, meta.Description)
	}
	if meta.Description != "Aww yeah, it's the Feather you've waited for." {
		t.Errorf("description = %q", meta.Description)
	}
	if meta.Title != "Adafruit HUZZAH32 – ESP32 Feather" {
		t.Errorf("title = %q", meta.Title)
	}
}

func TestUndecodableButValidImageIsKept(t *testing.T) {
	// Go's image/jpeg rejects some perfectly valid JPEGs ("unsupported JPEG
	// feature: luma/chroma subsampling ratio"), which real product photos and
	// camera output do hit. Losing the photo over that is worse than having no
	// thumbnail, so the original is stored and served in the thumbnail's place.
	app := newTestApp(t)

	valid := testJPEG(t, 40, 30, 0)
	undecodable := append([]byte(nil), valid[:6]...) // keep the JPEG magic
	undecodable = append(undecodable, []byte("garbage that no decoder accepts")...)

	name, err := app.photos.Save(bytes.NewReader(undecodable), "weird.jpg")
	if err != nil {
		t.Fatalf("a JPEG the decoder cannot read was rejected outright: %v", err)
	}
	if _, err := os.Stat(app.photos.Path(name)); err != nil {
		t.Fatalf("original was not stored: %v", err)
	}

	// Serving its thumbnail must fall back to the original rather than 404.
	srv := httptest.NewServer(app.routes())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/media/thumb/" + name)
	if err != nil {
		t.Fatalf("GET thumb: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("thumbnail request = %d, want 200 by falling back to the original", res.StatusCode)
	}

	// Genuinely non-image uploads are still refused.
	if _, err := app.photos.Save(strings.NewReader("<html>not an image</html>"), "page.html"); err == nil {
		t.Error("a non-image was accepted")
	}
}
