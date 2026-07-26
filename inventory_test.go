package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()

	db, err := openDB(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	photos, err := NewPhotoStore(filepath.Join(dir, "photos"))
	if err != nil {
		t.Fatalf("NewPhotoStore: %v", err)
	}
	auth, err := NewAuth("", filepath.Join(dir, "session.key"))
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}
	return &App{
		cfg:    Config{DataDir: dir, Title: "Test Bin"},
		store:  &Store{db: db},
		photos: photos,
		tmpl:   mustTemplates(),
		auth:   auth,
	}
}

func seed(t *testing.T, s *Store, items ...Item) []int64 {
	t.Helper()
	var ids []int64
	for _, it := range items {
		id, err := s.CreateItem(it)
		if err != nil {
			t.Fatalf("CreateItem(%s): %v", it.Name, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestSearchRequiresEveryWordToMatch(t *testing.T) {
	app := newTestApp(t)
	seed(t, app.store,
		Item{Name: "ESP32 devkit", Category: "MCU", Location: "Drawer 3", Quantity: 5},
		Item{Name: "ESP32-CAM", Category: "MCU", Location: "Shelf B", Quantity: 2},
		Item{Name: "Resistor 10k", Category: "Passive", Location: "Drawer 3", Quantity: 400, Tags: "smd, 0805"},
	)

	cases := []struct {
		term string
		want []string
	}{
		{"esp32", []string{"ESP32 devkit", "ESP32-CAM"}},
		{"esp32 drawer", []string{"ESP32 devkit"}}, // words AND across fields
		{"drawer 0805", []string{"Resistor 10k"}},  // name in one field, tag in another
		{"shelf 0805", nil},                        // no single item has both
		{"0805", []string{"Resistor 10k"}},         // matches via tags
		{"ESP32 CAM", []string{"ESP32-CAM"}},       // case-insensitive
		{"", []string{"ESP32 devkit", "ESP32-CAM", "Resistor 10k"}},
	}
	for _, tc := range cases {
		got, err := app.store.ListItems(Query{Search: tc.term, Sort: "name"})
		if err != nil {
			t.Fatalf("ListItems(%q): %v", tc.term, err)
		}
		var names []string
		for _, it := range got {
			names = append(names, it.Name)
		}
		if len(names) != len(tc.want) {
			t.Errorf("search %q = %v, want %v", tc.term, names, tc.want)
			continue
		}
		for _, w := range tc.want {
			if !contains(names, w) {
				t.Errorf("search %q = %v, missing %q", tc.term, names, w)
			}
		}
	}
}

func TestSearchTreatsWildcardsAsLiterals(t *testing.T) {
	app := newTestApp(t)
	seed(t, app.store,
		Item{Name: "ESP32 devkit"},
		Item{Name: "100% cotton wipes"},
	)
	got, err := app.store.ListItems(Query{Search: "%"})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(got) != 1 || got[0].Name != "100% cotton wipes" {
		t.Errorf(`search "%%" returned %d items, want just the literal match`, len(got))
	}
}

func TestFilterAndFacets(t *testing.T) {
	app := newTestApp(t)
	seed(t, app.store,
		Item{Name: "Pi 4B", Category: "SBC", Location: "Shelf B"},
		Item{Name: "Pi Zero", Category: "SBC", Location: "Drawer 3"},
		Item{Name: "M5StickC", Category: "MCU", Location: "Drawer 3"},
		Item{Name: "Loose wire"}, // no category or location
	)

	got, _ := app.store.ListItems(Query{Category: "SBC", Location: "Drawer 3"})
	if len(got) != 1 || got[0].Name != "Pi Zero" {
		t.Errorf("combined filter returned %d items, want just Pi Zero", len(got))
	}

	cats, err := app.store.Facets("category")
	if err != nil {
		t.Fatalf("Facets: %v", err)
	}
	want := map[string]int{"MCU": 1, "SBC": 2}
	if len(cats) != len(want) {
		t.Fatalf("facets = %+v, want %v (blank excluded)", cats, want)
	}
	for _, f := range cats {
		if want[f.Value] != f.Count {
			t.Errorf("facet %s = %d, want %d", f.Value, f.Count, want[f.Value])
		}
	}

	if _, err := app.store.Facets("notes; DROP TABLE items"); err == nil {
		t.Error("Facets accepted an arbitrary column name")
	}
}

func TestAdjustQuantityClampsAtZero(t *testing.T) {
	app := newTestApp(t)
	id := seed(t, app.store, Item{Name: "Cap 100nF", Quantity: 3})[0]

	for _, tc := range []struct{ delta, want int }{{5, 8}, {-2, 6}, {-100, 0}, {-1, 0}, {2, 2}} {
		got, err := app.store.AdjustQuantity(id, tc.delta)
		if err != nil {
			t.Fatalf("AdjustQuantity: %v", err)
		}
		if got != tc.want {
			t.Errorf("AdjustQuantity(%d) = %d, want %d", tc.delta, got, tc.want)
		}
	}
}

func TestDeleteItemReportsOrphanedPhotos(t *testing.T) {
	app := newTestApp(t)
	id := seed(t, app.store, Item{Name: "Board"})[0]
	for _, name := range []string{"aaaaaaaaaaaaaaaa.jpg", "bbbbbbbbbbbbbbbb.jpg"} {
		if err := app.store.AddPhoto(id, name); err != nil {
			t.Fatalf("AddPhoto: %v", err)
		}
	}
	files, err := app.store.DeleteItem(id)
	if err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("DeleteItem returned %v, want both filenames so they can be unlinked", files)
	}
	if _, err := app.store.GetItem(id); err == nil {
		t.Error("item still readable after delete")
	}
}

func TestSetCoverPhotoMovesPhotoToFront(t *testing.T) {
	app := newTestApp(t)
	id := seed(t, app.store, Item{Name: "Board"})[0]
	for _, n := range []string{"1111111111111111.jpg", "2222222222222222.jpg", "3333333333333333.jpg"} {
		app.store.AddPhoto(id, n)
	}
	it, _ := app.store.GetItem(id)
	third := it.Photos[2]

	if _, err := app.store.SetCoverPhoto(third.ID); err != nil {
		t.Fatalf("SetCoverPhoto: %v", err)
	}
	it, _ = app.store.GetItem(id)
	if it.Thumb() != third.Filename {
		t.Errorf("cover = %s, want %s", it.Thumb(), third.Filename)
	}
	if len(it.Photos) != 3 {
		t.Errorf("photo count changed to %d", len(it.Photos))
	}
}

func TestPhotoSaveWritesThumbnail(t *testing.T) {
	app := newTestApp(t)
	raw := testJPEG(t, 1200, 800, 0)

	name, err := app.photos.Save(bytes.NewReader(raw), "shot.jpg")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !safeName.MatchString(name) {
		t.Fatalf("generated name %q is rejected by the media handler's own guard", name)
	}

	cfg, _, err := decodeFile(app.photos.ThumbPath(name))
	if err != nil {
		t.Fatalf("thumbnail unreadable: %v", err)
	}
	if cfg.Width != thumbMax {
		t.Errorf("thumb width = %d, want longest edge scaled to %d", cfg.Width, thumbMax)
	}
	if cfg.Height != 341 { // 800 * 512/1200, truncated
		t.Errorf("thumb height = %d, want aspect ratio preserved (341)", cfg.Height)
	}

	app.photos.Remove(name)
	if _, _, err := decodeFile(app.photos.ThumbPath(name)); err == nil {
		t.Error("Remove left the thumbnail behind")
	}
}

func TestPhotoSaveRejectsNonImages(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.photos.Save(strings.NewReader("#!/bin/sh\nrm -rf /\n"), "evil.sh"); err == nil {
		t.Error("Save accepted a non-image upload")
	}
}

func TestThumbnailIsRotatedToMatchExif(t *testing.T) {
	app := newTestApp(t)
	// Orientation 6 means "rotate 90° CW to display": a 1200x800 landscape
	// original is shown by the browser as 800x1200 portrait, and the thumb has
	// to agree or the grid looks sideways next to the detail view.
	raw := testJPEG(t, 1200, 800, 6)

	name, err := app.photos.Save(bytes.NewReader(raw), "portrait.jpg")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg, _, err := decodeFile(app.photos.ThumbPath(name))
	if err != nil {
		t.Fatalf("thumbnail unreadable: %v", err)
	}
	if cfg.Width >= cfg.Height {
		t.Errorf("thumb is %dx%d, want portrait after applying EXIF orientation 6", cfg.Width, cfg.Height)
	}
}

func TestExifOrientationParsing(t *testing.T) {
	for _, want := range []int{1, 3, 6, 8} {
		if got := exifOrientation(testJPEG(t, 8, 8, want)); got != want {
			t.Errorf("exifOrientation = %d, want %d", got, want)
		}
	}
	if got := exifOrientation(testJPEG(t, 8, 8, 0)); got != 1 {
		t.Errorf("exifOrientation with no EXIF = %d, want 1", got)
	}
	if got := exifOrientation([]byte("not a jpeg at all")); got != 1 {
		t.Errorf("exifOrientation on junk = %d, want 1", got)
	}
}

func TestApplyOrientationSwapsAxes(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	src.Set(0, 0, color.RGBA{255, 0, 0, 255}) // top-left marker

	rotated := applyOrientation(src, 6) // 90° CW
	if b := rotated.Bounds(); b.Dx() != 2 || b.Dy() != 4 {
		t.Fatalf("rotated bounds = %v, want 2x4", b)
	}
	// Under a 90° CW rotation the old top-left pixel lands at the top-right.
	if r, _, _, _ := rotated.At(1, 0).RGBA(); r>>8 != 255 {
		t.Error("marker pixel did not land at the top-right corner")
	}
	if applyOrientation(src, 1).Bounds() != src.Bounds() {
		t.Error("orientation 1 should be a no-op")
	}
}

func TestNormalizeTags(t *testing.T) {
	cases := map[string]string{
		"wifi, 3v3 ,  , wifi": "wifi, 3v3",
		"  SMD ,smd,Smd  ":    "SMD",
		"":                    "",
		"a,b,c":               "a, b, c",
	}
	for in, want := range cases {
		if got := normalizeTags(in); got != want {
			t.Errorf("normalizeTags(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSafeNextBlocksOffsiteRedirects(t *testing.T) {
	cases := map[string]string{
		"/items/4":         "/items/4",
		"/?q=esp":          "/?q=esp",
		"//evil.com/x":     "/",
		"https://evil.com": "/",
		"evil.com":         "/",
		"":                 "/",
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMediaHandlerRejectsUnsafeNames(t *testing.T) {
	app := newTestApp(t)
	for _, name := range []string{"session.key", "../inventory.db", "..%2finventory.db", "abc.jpg", "deadbeefdeadbeef.exe"} {
		r := httptest.NewRequest(http.MethodGet, "/media/x", nil)
		r.SetPathValue("name", name)
		w := httptest.NewRecorder()
		app.handleMedia(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("GET /media/%s = %d, want 404", name, w.Code)
		}
	}
}

func TestCreateEditAndViewItemOverHTTP(t *testing.T) {
	app := newTestApp(t)
	srv := httptest.NewServer(app.routes())
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	// Create, with a photo attached in the same submit.
	body, ct := multipartForm(t, map[string]string{
		"name": "M5StickC Plus", "quantity": "4", "location": "Drawer 3",
		"category": "MCU", "tags": "esp32, display",
	}, "photos", "stick.jpg", testJPEG(t, 600, 400, 0))

	res, err := client.Post(srv.URL+"/items", ct, body)
	if err != nil {
		t.Fatalf("POST /items: %v", err)
	}
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /items = %d, want 303", res.StatusCode)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	if q := loc.Query().Get("error"); q != "" {
		t.Fatalf("create reported an error: %s", q)
	}

	// The detail page shows it, photo and all.
	page := getBody(t, client, srv.URL+loc.Path)
	for _, want := range []string{"M5StickC Plus", "Drawer 3", "/media/", "esp32"} {
		if !strings.Contains(page, want) {
			t.Errorf("detail page missing %q", want)
		}
	}

	// The grid shows it, and the filter chips are populated from the item.
	grid := getBody(t, client, srv.URL+"/")
	if !strings.Contains(grid, "M5StickC Plus") || !strings.Contains(grid, "/media/thumb/") {
		t.Error("grid is missing the new item or its thumbnail")
	}

	// A blank name is rejected rather than saved.
	form := url.Values{"name": {"  "}, "quantity": {"1"}}
	res, err = client.PostForm(srv.URL+"/items", form)
	if err != nil {
		t.Fatalf("POST blank name: %v", err)
	}
	if !strings.Contains(res.Header.Get("Location"), "error=") {
		t.Error("blank name was accepted")
	}
}

func TestQuantityEndpointAnswersJSON(t *testing.T) {
	app := newTestApp(t)
	id := seed(t, app.store, Item{Name: "Header pins", Quantity: 10})[0]
	srv := httptest.NewServer(app.routes())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/items/"+strconv.FormatInt(id, 10)+"/quantity", strings.NewReader("delta=-4"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST quantity: %v", err)
	}
	defer res.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(res.Body)
	if got := strings.TrimSpace(buf.String()); got != `{"quantity":6}` {
		t.Errorf("quantity response = %s, want {\"quantity\":6}", got)
	}
}

func TestAuthGatesEverythingButLoginAndHealth(t *testing.T) {
	app := newTestApp(t)
	auth, err := NewAuth("hunter2", filepath.Join(app.cfg.DataDir, "session.key"))
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}
	app.auth = auth
	srv := httptest.NewServer(app.routes())
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	res, _ := client.Get(srv.URL + "/")
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/login") {
		t.Errorf("anonymous GET / = %d %s, want redirect to /login", res.StatusCode, res.Header.Get("Location"))
	}

	res, _ = client.PostForm(srv.URL+"/items/1/quantity", url.Values{"delta": {"1"}})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("anonymous POST = %d, want 403", res.StatusCode)
	}

	res, _ = client.Get(srv.URL + "/healthz")
	if res.StatusCode != http.StatusOK {
		t.Errorf("healthz = %d, want 200 without a session", res.StatusCode)
	}

	// A forged cookie must not pass the HMAC check.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "99999999999.deadbeef"})
	res, _ = client.Do(req)
	if res.StatusCode != http.StatusSeeOther {
		t.Errorf("forged cookie = %d, want redirect to /login", res.StatusCode)
	}

	// The real password issues a session that works.
	res, _ = client.PostForm(srv.URL+"/login", url.Values{"password": {"hunter2"}})
	cookies := res.Cookies()
	if len(cookies) == 0 {
		t.Fatal("login set no cookie")
	}
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(cookies[0])
	res, _ = client.Do(req)
	if res.StatusCode != http.StatusOK {
		t.Errorf("authenticated GET / = %d, want 200", res.StatusCode)
	}
}

// --- helpers ----------------------------------------------------------------

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func decodeFile(path string) (image.Config, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return image.Config{}, "", err
	}
	defer f.Close()
	return image.DecodeConfig(f)
}

// testJPEG renders a solid image and, when orientation is 1..8, splices in a
// minimal EXIF APP1 segment carrying that orientation tag.
func testJPEG(t *testing.T, w, h, orientation int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode: %v", err)
	}
	raw := buf.Bytes()
	if orientation < 1 || orientation > 8 {
		return raw
	}

	tiff := new(bytes.Buffer)
	tiff.WriteString("II")                              // little-endian
	binary.Write(tiff, binary.LittleEndian, uint16(42)) // magic
	binary.Write(tiff, binary.LittleEndian, uint32(8))  // IFD0 offset
	binary.Write(tiff, binary.LittleEndian, uint16(1))  // one entry
	binary.Write(tiff, binary.LittleEndian, uint16(0x0112))
	binary.Write(tiff, binary.LittleEndian, uint16(3)) // SHORT
	binary.Write(tiff, binary.LittleEndian, uint32(1))
	binary.Write(tiff, binary.LittleEndian, uint16(orientation))
	binary.Write(tiff, binary.LittleEndian, uint16(0)) // pad value field
	binary.Write(tiff, binary.LittleEndian, uint32(0)) // no next IFD

	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(payload)+2))

	out := append([]byte{0xFF, 0xD8}, app1...)
	out = append(out, payload...)
	return append(out, raw[2:]...) // original minus its SOI
}

func multipartForm(t *testing.T, fields map[string]string, fileField, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := new(bytes.Buffer)
	mw := multipart.NewWriter(body)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	if fileField != "" {
		fw, err := mw.CreateFormFile(fileField, filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		fw.Write(content)
	}
	mw.Close()
	return body, mw.FormDataContentType()
}

func getBody(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	res, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", url, res.StatusCode)
	}
	buf := new(bytes.Buffer)
	buf.ReadFrom(res.Body)
	return buf.String()
}
