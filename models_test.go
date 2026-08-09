package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image/png"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// --- STL parsing ------------------------------------------------------------

// binarySTLCube writes a real binary STL of a box, so the parser is tested
// against the format rather than against a fixture of its own making.
func binarySTLCube(t *testing.T, sx, sy, sz float64) []byte {
	t.Helper()
	// Eight corners, twelve triangles.
	v := [8][3]float64{
		{0, 0, 0}, {sx, 0, 0}, {sx, sy, 0}, {0, sy, 0},
		{0, 0, sz}, {sx, 0, sz}, {sx, sy, sz}, {0, sy, sz},
	}
	faces := [12][3]int{
		{0, 2, 1}, {0, 3, 2}, // bottom
		{4, 5, 6}, {4, 6, 7}, // top
		{0, 1, 5}, {0, 5, 4},
		{1, 2, 6}, {1, 6, 5},
		{2, 3, 7}, {2, 7, 6},
		{3, 0, 4}, {3, 4, 7},
	}

	var b bytes.Buffer
	b.Write(make([]byte, 80)) // header
	binary.Write(&b, binary.LittleEndian, uint32(len(faces)))
	for _, f := range faces {
		for i := 0; i < 3; i++ { // the normal, which the parser ignores
			binary.Write(&b, binary.LittleEndian, float32(0))
		}
		for _, idx := range f {
			for axis := 0; axis < 3; axis++ {
				binary.Write(&b, binary.LittleEndian, float32(v[idx][axis]))
			}
		}
		binary.Write(&b, binary.LittleEndian, uint16(0))
	}
	return b.Bytes()
}

const asciiSTLTriangle = `solid tri
  facet normal 0 0 1
    outer loop
      vertex 0 0 0
      vertex 10 0 0
      vertex 0 20 5
    endloop
  endfacet
endsolid tri
`

func TestParseSTLBinaryAndASCII(t *testing.T) {
	m, err := ParseSTL(binarySTLCube(t, 60, 31, 48))
	if err != nil {
		t.Fatalf("binary STL: %v", err)
	}
	if !m.Binary {
		t.Error("a binary STL was read as ASCII")
	}
	if m.TriangleCount() != 12 {
		t.Errorf("got %d triangles, want 12", m.TriangleCount())
	}
	if got := m.Dimensions(); got != "60.0 × 31.0 × 48.0 mm" {
		t.Errorf("dimensions = %q", got)
	}
	if !m.FitsIn(220, 220, 250) {
		t.Error("a 60x31x48 box should fit a 220x220x250 bed")
	}
	if m.FitsIn(50, 50, 250) {
		t.Error("a 60 mm box should not fit a 50 mm bed")
	}
	// The long way round counts: 31 x 60 fits a bed that 60 x 31 also fits, but
	// a 40 x 70 bed only takes it one way.
	if !m.FitsIn(40, 70, 250) {
		t.Error("the model should be allowed to be turned 90 degrees on the bed")
	}

	a, err := ParseSTL([]byte(asciiSTLTriangle))
	if err != nil {
		t.Fatalf("ASCII STL: %v", err)
	}
	if a.Binary {
		t.Error("an ASCII STL was read as binary")
	}
	if a.TriangleCount() != 1 {
		t.Fatalf("got %d triangles, want 1", a.TriangleCount())
	}
	if got := a.Dimensions(); got != "10.0 × 20.0 × 5.0 mm" {
		t.Errorf("ASCII dimensions = %q", got)
	}
}

func TestParseSTLDoesNotTrustTheLeadingWord(t *testing.T) {
	// Plenty of exporters write "solid" into a binary file's 80-byte header.
	// Reading that as ASCII yields nothing, so the length is what must decide.
	raw := binarySTLCube(t, 10, 10, 10)
	copy(raw, "solid ExportedBySomethingCareless")

	m, err := ParseSTL(raw)
	if err != nil {
		t.Fatalf("binary STL with a solid header: %v", err)
	}
	if !m.Binary || m.TriangleCount() != 12 {
		t.Errorf("read as binary=%v with %d triangles, want binary with 12", m.Binary, m.TriangleCount())
	}
}

func TestParseSTLRefusesRubbish(t *testing.T) {
	cases := map[string][]byte{
		"empty":        {},
		"short":        []byte("solid"),
		"not an STL":   []byte("this is a text file that goes on for a while but is not a model"),
		"a PNG":        testPNG(t),
		"no triangles": []byte("solid empty\nendsolid empty\n"),
	}
	for name, raw := range cases {
		if m, err := ParseSTL(raw); err == nil {
			t.Errorf("ParseSTL(%s) returned %d triangles, want a refusal", name, m.TriangleCount())
		}
	}
}

func TestRenderSTLProducesAPicture(t *testing.T) {
	m, err := ParseSTL(binarySTLCube(t, 40, 40, 40))
	if err != nil {
		t.Fatalf("ParseSTL: %v", err)
	}
	raw, err := m.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("the render is not a readable PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != stlWidth || b.Dy() != stlHeight {
		t.Errorf("render is %dx%d, want %dx%d", b.Dx(), b.Dy(), stlWidth, stlHeight)
	}

	// A cube seen from the corner shows three faces at three brightnesses, and
	// covers a good part of the frame. Both are worth checking: a render that
	// silently produced an empty background would otherwise pass.
	shades := map[uint32]int{}
	painted := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 == 0x1b && g>>8 == 0x1e && bl>>8 == 0x25 {
				continue // background
			}
			painted++
			shades[r>>8]++
		}
	}
	if painted < stlWidth*stlHeight/8 {
		t.Errorf("only %d pixels were painted; the model is missing or tiny", painted)
	}
	if len(shades) < 3 {
		t.Errorf("the cube rendered in %d shades, want three faces lit differently", len(shades))
	}

	// The model should be the right way up: with the light from above, the top
	// face is the brightest, and it must sit above the middle of the frame.
	var brightest uint32
	for shade := range shades {
		if shades[shade] > 200 && shade > brightest {
			brightest = shade
		}
	}
	topRow, bottomRow := 0, 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if r, _, _, _ := img.At(x, y).RGBA(); r>>8 == brightest {
				if y < stlHeight/2 {
					topRow++
				} else {
					bottomRow++
				}
			}
		}
	}
	if topRow <= bottomRow {
		t.Errorf("the brightest face is mostly below the middle (%d above, %d below) — the model is upside down",
			topRow, bottomRow)
	}
}

func TestRenderHandlesADegenerateMesh(t *testing.T) {
	// Three collinear points: zero area, and every rasteriser's favourite way
	// to divide by zero.
	flat := "solid f\nfacet normal 0 0 1\nouter loop\n" +
		"vertex 0 0 0\nvertex 1 1 1\nvertex 2 2 2\nendloop\nendfacet\nendsolid f\n"
	m, err := ParseSTL([]byte(flat))
	if err != nil {
		t.Fatalf("ParseSTL: %v", err)
	}
	if _, err := m.Render(); err != nil && !strings.Contains(err.Error(), "no size") {
		t.Errorf("Render on a degenerate mesh: %v", err)
	}
}

// --- searching --------------------------------------------------------------

// stubPrintables answers the same GraphQL shape the real endpoint does.
func stubPrintables(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Printables was queried with %s, want POST", r.Method)
		}
		body := readAll(t, r.Body)
		if !strings.Contains(body, "searchPrints2") {
			t.Errorf("query did not use searchPrints2: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"searchPrints2":{"items":[
			{"id":"742926","name":"Raspberry Pi 5 case","slug":"raspberry-pi-5-case",
			 "ratingAvg":"4.8971962617","likesCount":1601,"downloadCount":21889,"nsfw":false,
			 "image":{"filePath":"media/prints/742926/images/render.jpg"},
			 "user":{"publicUsername":"Stamos"}},
			{"id":"1","name":"Something not safe","slug":"nope","ratingAvg":"5","likesCount":1,
			 "downloadCount":1,"nsfw":true,"image":null,"user":null}
		]}}}`)
	}))
}

func TestPrintablesSearchReadsResults(t *testing.T) {
	srv := stubPrintables(t)
	defer srv.Close()

	p := NewPrintablesProvider(NewFetcher(true))
	p.endpoint = srv.URL

	got, err := p.SearchModels(context.Background(), "raspberry pi 5 case", 5)
	if err != nil {
		t.Fatalf("SearchModels: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1 — the NSFW one should be dropped", len(got))
	}
	r := got[0]
	if r.Title != "Raspberry Pi 5 case" || r.Author != "Stamos" {
		t.Errorf("result = %+v", r)
	}
	if r.URL != "https://www.printables.com/model/742926-raspberry-pi-5-case" {
		t.Errorf("URL = %q", r.URL)
	}
	if r.ImageURL != "https://media.printables.com/media/prints/742926/images/render.jpg" {
		t.Errorf("image = %q", r.ImageURL)
	}
	if math.Abs(r.Rating-4.897) > 0.001 {
		t.Errorf("rating = %v", r.Rating)
	}
	if !strings.Contains(r.Stats(), "21.9k downloads") {
		t.Errorf("stats = %q, want the download count in it", r.Stats())
	}
}

func TestPrintablesPassesTheErrorThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"errors":[{"message":"Unknown argument 'cursor' on field 'Query.searchPrints2'."}]}`)
	}))
	defer srv.Close()

	p := NewPrintablesProvider(NewFetcher(true))
	p.endpoint = srv.URL
	_, err := p.SearchModels(context.Background(), "case", 5)
	if err == nil || !strings.Contains(err.Error(), "Unknown argument") {
		t.Errorf("error = %v, want the endpoint's own message passed through", err)
	}
}

func TestThingiverseIsSilentWithoutAToken(t *testing.T) {
	hub := NewModelHub(NewFetcher(true), Config{})
	if got := strings.Join(hub.Sources(), ","); got != "Printables" {
		t.Errorf("sources = %q, want only Printables when no token is set", got)
	}
	hub = NewModelHub(NewFetcher(true), Config{ThingiverseToken: "abc"})
	if len(hub.Sources()) != 2 {
		t.Errorf("sources = %v, want Thingiverse to join once it has a token", hub.Sources())
	}
}

func TestThingiverseSendsTheTokenAsAHeader(t *testing.T) {
	var auth, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		query = r.URL.RawQuery + " " + r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hits":[{"id":42,"name":"ESP32 case","public_url":"https://www.thingiverse.com/thing:42",
			"thumbnail":"https://cdn.thingiverse.com/t.jpg","like_count":12,"download_count":340,
			"is_nsfw":false,"creator":{"name":"someone"},"license":"CC BY 4.0"}]}`)
	}))
	defer srv.Close()

	p := NewThingiverseProvider(NewFetcher(true), "secret-token")
	p.endpoint = srv.URL
	got, err := p.SearchModels(context.Background(), "esp32 case", 5)
	if err != nil {
		t.Fatalf("SearchModels: %v", err)
	}
	if auth != "Bearer secret-token" {
		t.Errorf("Authorization = %q", auth)
	}
	// A token in the query string ends up in logs at the far end and in our own
	// error messages; it must not be there.
	if strings.Contains(query, "secret-token") {
		t.Errorf("the token leaked into the URL: %s", query)
	}
	if len(got) != 1 || got[0].Licence != "CC BY 4.0" || got[0].Downloads != 340 {
		t.Errorf("result = %+v", got)
	}
}

func TestModelHubKeepsGoingWhenASourceFails(t *testing.T) {
	good := stubPrintables(t)
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer bad.Close()

	p := NewPrintablesProvider(NewFetcher(true))
	p.endpoint = good.URL
	tv := NewThingiverseProvider(NewFetcher(true), "t")
	tv.endpoint = bad.URL

	hub := &ModelHub{providers: []ModelProvider{p, tv}}
	report := hub.Search(context.Background(), "case", 5)
	if len(report.Results) != 1 {
		t.Errorf("got %d results, want the working source's one", len(report.Results))
	}
	if len(report.Notes) != 1 || !strings.Contains(report.Notes[0], "Thingiverse") {
		t.Errorf("notes = %v, want the broken source named", report.Notes)
	}
}

func TestModelSearchTermsStartWithThePartNumber(t *testing.T) {
	got := ModelSearchTerms(Item{Name: "ESP32 devkit", PartNumber: "ESP32-WROOM-32"})
	if len(got) == 0 || got[0] != "ESP32-WROOM-32 case" {
		t.Errorf("terms = %v, want the part number first", got)
	}
	if !contains(got, "ESP32 devkit enclosure") || !contains(got, "ESP32 devkit mount") {
		t.Errorf("terms = %v, want enclosure and mount among them", got)
	}
	// No part number, no duplicate suggestions.
	got = ModelSearchTerms(Item{Name: "Widget"})
	seen := map[string]bool{}
	for _, s := range got {
		if seen[s] {
			t.Errorf("duplicate suggestion %q in %v", s, got)
		}
		seen[s] = true
	}
}

// --- attaching and measuring ------------------------------------------------

func TestKeepAModelAndMeasureItThroughTheWeb(t *testing.T) {
	app := newTestApp(t)
	ids := seed(t, app.store, Item{Name: "Raspberry Pi 5", Quantity: 1})

	// A site serving a thumbnail and, separately, an STL.
	assets := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/render.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(testPNG(t))
		case "/case.stl":
			w.Header().Set("Content-Type", "model/stl")
			w.Write(binarySTLCube(t, 95, 65, 30))
		default:
			http.NotFound(w, r)
		}
	}))
	defer assets.Close()

	srv := httptest.NewServer(app.routes())
	defer srv.Close()

	res, err := http.PostForm(srv.URL+fmt.Sprintf("/items/%d/models", ids[0]), map[string][]string{
		"url":       {"https://www.printables.com/model/742926-raspberry-pi-5-case"},
		"title":     {"Raspberry Pi 5 case"},
		"source":    {"Printables"},
		"author":    {"Stamos"},
		"image_url": {assets.URL + "/render.png"},
		"downloads": {"21889"},
		"rating":    {"4.90"},
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	res.Body.Close()

	it, _ := app.store.GetItem(ids[0])
	if len(it.Models) != 1 {
		t.Fatalf("item has %d models, want 1", len(it.Models))
	}
	m := it.Models[0]
	if m.Title != "Raspberry Pi 5 case" || m.Author != "Stamos" || m.Downloads != 21889 {
		t.Errorf("stored model = %+v", m)
	}
	if m.Thumb == "" {
		t.Error("the site's render was not downloaded")
	}
	if m.Measured() {
		t.Error("a model with no STL yet should not claim to be measured")
	}
	if m.Image() != m.Thumb {
		t.Error("with no render of our own, the site's picture is the one to show")
	}

	// Now give it the geometry.
	res, err = http.PostForm(srv.URL+fmt.Sprintf("/models/%d/mesh", m.ID),
		map[string][]string{"stl_url": {assets.URL + "/case.stl"}})
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	res.Body.Close()
	if final := res.Request.URL.String(); strings.Contains(final, "error=") {
		t.Fatalf("measuring failed: %s", final)
	}

	it, _ = app.store.GetItem(ids[0])
	m = it.Models[0]
	if !m.Measured() {
		t.Fatal("the model was not measured")
	}
	if m.Dimensions != "95.0 × 65.0 × 30.0 mm" {
		t.Errorf("dimensions = %q", m.Dimensions)
	}
	if m.Triangles != 12 {
		t.Errorf("triangles = %d", m.Triangles)
	}
	if m.Preview == "" {
		t.Error("no render was stored")
	}
	if m.Image() != m.Preview {
		t.Error("our own render of the geometry should win over the site's picture")
	}
	// The STL itself is deliberately not kept.
	if _, err := app.store.GetModel(m.ID); err != nil {
		t.Fatalf("GetModel: %v", err)
	}

	// Removing it takes both pictures with it.
	itemID, files, err := app.store.DeleteModel(m.ID)
	if err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	if itemID != ids[0] || len(files) != 2 {
		t.Errorf("delete reported item %d and %d files, want %d and 2", itemID, len(files), ids[0])
	}
}

func TestMeasuringRefusesSomethingThatIsNotAModel(t *testing.T) {
	app := newTestApp(t)
	ids := seed(t, app.store, Item{Name: "Raspberry Pi 5", Quantity: 1})
	modelID, err := app.store.AddModel(Model{
		ItemID: ids[0], Title: "A case", URL: "https://example.com/x", Source: "Printables",
	})
	if err != nil {
		t.Fatalf("AddModel: %v", err)
	}

	srv := httptest.NewServer(app.routes())
	defer srv.Close()

	body := new(bytes.Buffer)
	mw := multipart.NewWriter(body)
	part, _ := mw.CreateFormFile("stl", "notamodel.stl")
	part.Write([]byte("this is definitely not an STL file, it is just some words"))
	mw.Close()

	res, err := http.Post(srv.URL+fmt.Sprintf("/models/%d/mesh", modelID), mw.FormDataContentType(), body)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	res.Body.Close()
	if final := res.Request.URL.String(); !strings.Contains(final, "error=") {
		t.Errorf("landed on %q, want a refusal", final)
	}
	m, _ := app.store.GetModel(modelID)
	if m.Measured() {
		t.Error("a refused measurement still recorded dimensions")
	}
}

func TestOversizedModelIsFlagged(t *testing.T) {
	m, err := ParseSTL(binarySTLCube(t, 300, 300, 40))
	if err != nil {
		t.Fatalf("ParseSTL: %v", err)
	}
	if m.FitsIn(220, 220, 250) {
		t.Error("a 300 mm model should not be reported as fitting a 220 mm bed")
	}
}

func TestModelPagesRender(t *testing.T) {
	app := newTestApp(t)
	ids := seed(t, app.store, Item{Name: "Raspberry Pi 5", PartNumber: "RPI5", Quantity: 1})
	if _, err := app.store.AddModel(Model{
		ItemID: ids[0], Title: "Pi 5 case", URL: "https://www.printables.com/model/1-x",
		Source: "Printables", Author: "Stamos", Downloads: 21889, Rating: 4.9,
		Dimensions: "95.0 × 65.0 × 30.0 mm", Triangles: 4210,
	}); err != nil {
		t.Fatalf("AddModel: %v", err)
	}

	srv := httptest.NewServer(app.routes())
	defer srv.Close()

	// The search page with no configured network still has to render: the hub
	// in tests reaches nothing, so this is the "every source failed" path.
	for _, path := range []string{
		fmt.Sprintf("/items/%d", ids[0]),
		fmt.Sprintf("/items/%d/models", ids[0]),
		fmt.Sprintf("/items/%d/models?q=pi+5+case", ids[0]),
	} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body := readAll(t, res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, res.StatusCode)
			continue
		}
		if strings.Contains(body, "<no value>") || strings.Contains(body, "ERROR:") {
			t.Errorf("GET %s rendered a template error:\n%s", path, firstLines(body, 30))
		}
	}

	res, _ := http.Get(srv.URL + fmt.Sprintf("/items/%d", ids[0]))
	body := readAll(t, res.Body)
	res.Body.Close()
	for _, want := range []string{"Pi 5 case", "95.0 × 65.0 × 30.0 mm", "21.9k downloads"} {
		if !strings.Contains(body, want) {
			t.Errorf("the item page does not show %q", want)
		}
	}

	// The sites that cannot be queried are still offered as browser links.
	res, _ = http.Get(srv.URL + fmt.Sprintf("/items/%d/models?q=pi+case", ids[0]))
	body = readAll(t, res.Body)
	res.Body.Close()
	for _, want := range []string{"MakerWorld", "Thangs", "Cults3D"} {
		if !strings.Contains(body, want) {
			t.Errorf("the search page does not offer %q", want)
		}
	}
}

func TestThumbnailProxyOnlyServesTheModelSites(t *testing.T) {
	app := newTestApp(t)
	srv := httptest.NewServer(app.routes())
	defer srv.Close()

	// Somewhere that is not a model site, even though it is a perfectly good
	// image: the proxy is for the sites we search, not for the network.
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(testPNG(t))
	}))
	defer elsewhere.Close()

	for _, bad := range []string{
		elsewhere.URL + "/x.png",
		"http://169.254.169.254/latest/meta-data/",
		"http://localhost:8080/admin/backup.zip",
		"file:///etc/passwd",
		"not a url",
	} {
		res, err := http.Get(srv.URL + "/media/remote?u=" + url.QueryEscape(bad))
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			t.Errorf("the proxy served %q; it should only serve the model sites", bad)
		}
	}
}
