package main

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// page carries everything the layout template needs on every render.
type page struct {
	Title    string
	SiteName string
	AuthOn   bool
	Flash    string
	Error    string
	Bare     bool // hides the nav, for the sign-in page
	Data     any
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, title string, data any) {
	p := page{
		Title:    title,
		SiteName: a.cfg.Title,
		AuthOn:   a.auth.Enabled(),
		Flash:    r.URL.Query().Get("flash"),
		Error:    r.URL.Query().Get("error"),
		Bare:     name == "login.html",
		Data:     data,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tmpl.ExecuteTemplate(w, name, p); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (a *App) fail(w http.ResponseWriter, err error, status int) {
	log.Printf("error: %v", err)
	http.Error(w, err.Error(), status)
}

// redirect sends the browser somewhere with an optional flash message. Using
// the URL rather than a session avoids any server-side flash storage.
func redirect(w http.ResponseWriter, r *http.Request, path, flash, errMsg string) {
	u, _ := url.Parse(path)
	q := u.Query()
	if flash != "" {
		q.Set("flash", flash)
	}
	if errMsg != "" {
		q.Set("error", errMsg)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

// --- grid -------------------------------------------------------------------

type indexData struct {
	Items      []Item
	Categories []Facet
	Locations  []Facet
	Tags       []Facet
	Folders    []*Folder
	Stats      Stats
	Query      Query
	Filtered   bool
}

// queryFromRequest reads the shared filter state used by the grid and by each
// folder view.
func (a *App) queryFromRequest(r *http.Request) Query {
	v := r.URL.Query()
	q := Query{
		Search:   strings.TrimSpace(v.Get("q")),
		Category: v.Get("category"),
		Location: v.Get("location"),
		Sort:     v.Get("sort"),
		Unfiled:  v.Get("unfiled") == "1",
	}
	for _, t := range v["tag"] {
		if t = strings.TrimSpace(t); t != "" {
			q.Tags = append(q.Tags, t)
		}
	}
	return q
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	q := a.queryFromRequest(r)
	items, err := a.store.ListItems(q)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	cats, err := a.store.Facets("category")
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	locs, err := a.store.Facets("location")
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	stats, err := a.store.Stats()
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}

	tags, err := a.store.TagFacets()
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	tree, err := a.store.FolderTree()
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}

	a.render(w, r, "index.html", "All items", indexData{
		Items:      items,
		Categories: cats,
		Locations:  locs,
		Tags:       tags,
		Folders:    FlattenFolders(tree),
		Stats:      stats,
		Query:      q,
		Filtered:   q.Any(),
	})
}

// --- single item ------------------------------------------------------------

type itemData struct {
	Item
	Folders []*Folder
}

func (a *App) handleItem(w http.ResponseWriter, r *http.Request) {
	it, ok := a.lookup(w, r)
	if !ok {
		return
	}
	a.render(w, r, "item.html", it.Name, itemData{Item: it, Folders: a.folderListOrNil()})
}

func (a *App) handleNewForm(w http.ResponseWriter, r *http.Request) {
	// Prefill from the view the person came from, so adding an item while
	// looking at a folder files it there by default.
	item := Item{
		Quantity: 1,
		Location: r.URL.Query().Get("location"),
		Category: r.URL.Query().Get("category"),
		FolderID: optionalID(r.URL.Query().Get("folder")),
	}
	if tag := strings.TrimSpace(r.URL.Query().Get("tag")); tag != "" {
		item.Tags = []Tag{{Name: tag, Icon: IconForTag(tag)}}
	}
	a.render(w, r, "edit.html", "New item", editData{
		Item:       item,
		Categories: a.facetsOrNil("category"),
		Locations:  a.facetsOrNil("location"),
		Tags:       a.tagFacetsOrNil(),
		Folders:    a.folderListOrNil(),
		IsNew:      true,
	})
}

func (a *App) facetsOrNil(column string) []Facet {
	f, err := a.store.Facets(column)
	if err != nil {
		log.Printf("facets %s: %v", column, err)
	}
	return f
}

func (a *App) tagFacetsOrNil() []Facet {
	f, err := a.store.TagFacets()
	if err != nil {
		log.Printf("tag facets: %v", err)
	}
	return f
}

func (a *App) folderListOrNil() []*Folder {
	tree, err := a.store.FolderTree()
	if err != nil {
		log.Printf("folder tree: %v", err)
		return nil
	}
	return FlattenFolders(tree)
}

type editData struct {
	Item       Item
	Categories []Facet
	Locations  []Facet
	Tags       []Facet
	Folders    []*Folder
	IsNew      bool
}

func (a *App) handleEditForm(w http.ResponseWriter, r *http.Request) {
	it, ok := a.lookup(w, r)
	if !ok {
		return
	}
	a.render(w, r, "edit.html", "Edit "+it.Name, editData{
		Item:       it,
		Categories: a.facetsOrNil("category"),
		Locations:  a.facetsOrNil("location"),
		Tags:       a.tagFacetsOrNil(),
		Folders:    a.folderListOrNil(),
	})
}

// itemFromForm reads the shared add/edit form. Quantity falls back to 0 rather
// than erroring, so a blank field never blocks a save.
func itemFromForm(r *http.Request) (Item, error) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		return Item{}, errors.New("name is required")
	}
	qty, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("quantity")))
	if qty < 0 {
		qty = 0
	}
	// The tag field is a comma-separated text input; the picker below it posts
	// additional values under the same name.
	var tags []Tag
	for _, name := range splitTags(strings.Join(append(r.Form["tags"], r.Form["tag"]...), ",")) {
		tags = append(tags, Tag{Name: name, Icon: IconForTag(name)})
	}

	return Item{
		Name:       name,
		Category:   strings.TrimSpace(r.FormValue("category")),
		Quantity:   qty,
		Location:   strings.TrimSpace(r.FormValue("location")),
		PartNumber: strings.TrimSpace(r.FormValue("part_number")),
		Value:      strings.TrimSpace(r.FormValue("value")),
		Tags:       tags,
		Link:       strings.TrimSpace(r.FormValue("link")),
		Notes:      strings.TrimSpace(r.FormValue("notes")),
		FolderID:   optionalID(r.FormValue("folder_id")),
	}, nil
}

func (a *App) handleCreate(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		a.fail(w, err, http.StatusBadRequest)
		return
	}
	it, err := itemFromForm(r)
	if err != nil {
		redirect(w, r, "/items/new", "", err.Error())
		return
	}
	id, err := a.store.CreateItem(it)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	msg := a.savePhotos(r, id)
	if urlMsg := a.savePhotoFromURL(r, id, r.FormValue("image_url")); urlMsg != "" {
		msg = strings.TrimPrefix(msg+"; "+urlMsg, "; ")
	}
	a.savePriceFromForm(r, id)
	redirect(w, r, fmt.Sprintf("/items/%d", id), "Added "+it.Name, msg)
}

func (a *App) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := parseForm(r); err != nil {
		a.fail(w, err, http.StatusBadRequest)
		return
	}
	it, err := itemFromForm(r)
	if err != nil {
		redirect(w, r, fmt.Sprintf("/items/%d/edit", id), "", err.Error())
		return
	}
	it.ID = id
	if err := a.store.UpdateItem(it); err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	msg := a.savePhotos(r, id)
	if urlMsg := a.savePhotoFromURL(r, id, r.FormValue("image_url")); urlMsg != "" {
		msg = strings.TrimPrefix(msg+"; "+urlMsg, "; ")
	}
	a.savePriceFromForm(r, id)
	redirect(w, r, fmt.Sprintf("/items/%d", id), "Saved", msg)
}

// savePriceFromForm records the optional price on the add/edit form. A price
// with no source still counts -- it is filed against the link's host, or
// "Unknown" -- because losing a figure someone typed is worse than guessing.
func (a *App) savePriceFromForm(r *http.Request, itemID int64) {
	amount, err := parsePrice(r.FormValue("price"))
	if err != nil || amount <= 0 {
		return
	}
	source := strings.TrimSpace(r.FormValue("price_source"))
	if source == "" {
		source = sourceFromURL(r.FormValue("link"))
	}
	if err := a.store.SetPrice(itemID, Price{
		Source:   source,
		Amount:   amount,
		Currency: strings.ToUpper(orDefault(r.FormValue("currency"), "USD")),
		URL:      strings.TrimSpace(r.FormValue("link")),
	}); err != nil {
		log.Printf("save price: %v", err)
	}
}

// sourceFromURL turns a product link into a shop name, so a price imported
// from a page is filed under something recognisable.
func sourceFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "Unknown"
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	if host == "" {
		return "Unknown"
	}
	return strings.ToUpper(host[:1]) + host[1:]
}

func (a *App) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	files, err := a.store.DeleteItem(id)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	for _, f := range files {
		a.photos.Remove(f)
	}
	redirect(w, r, "/", "Item deleted", "")
}

// handleQuantity backs the +/- buttons. It answers JSON for the inline path and
// falls back to a redirect when JavaScript is off.
func (a *App) handleQuantity(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	delta, err := strconv.Atoi(r.FormValue("delta"))
	if err != nil {
		a.fail(w, fmt.Errorf("bad delta: %w", err), http.StatusBadRequest)
		return
	}
	qty, err := a.store.AdjustQuantity(id, delta)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"quantity": qty})
		return
	}
	back := r.FormValue("return")
	if back == "" {
		back = fmt.Sprintf("/items/%d", id)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// --- photos -----------------------------------------------------------------

func (a *App) handleUploadPhotos(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := parseForm(r); err != nil {
		a.fail(w, err, http.StatusBadRequest)
		return
	}
	msg := a.savePhotos(r, id)
	redirect(w, r, fmt.Sprintf("/items/%d", id), "Photos updated", msg)
}

// savePhotos stores every uploaded file against an item and returns a
// human-readable summary of anything that failed (empty when all went well).
func (a *App) savePhotos(r *http.Request, itemID int64) string {
	if r.MultipartForm == nil {
		return ""
	}
	var failures []string
	for _, fh := range r.MultipartForm.File["photos"] {
		if fh.Size == 0 {
			continue
		}
		f, err := fh.Open()
		if err != nil {
			failures = append(failures, fh.Filename+": "+err.Error())
			continue
		}
		name, err := a.photos.Save(f, fh.Filename)
		f.Close()
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if err := a.store.AddPhoto(itemID, name); err != nil {
			a.photos.Remove(name)
			failures = append(failures, fh.Filename+": "+err.Error())
		}
	}
	if len(failures) == 0 {
		return ""
	}
	return strings.Join(failures, "; ")
}

func (a *App) handleDeletePhoto(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	itemID, filename, err := a.store.DeletePhoto(id)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	a.photos.Remove(filename)
	redirect(w, r, fmt.Sprintf("/items/%d", itemID), "Photo removed", "")
}

func (a *App) handleCoverPhoto(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	itemID, err := a.store.SetCoverPhoto(id)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	redirect(w, r, fmt.Sprintf("/items/%d", itemID), "Cover photo set", "")
}

func (a *App) handleMedia(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !safeName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	// Filenames are random and never reused, so they can cache forever.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, a.photos.Path(name))
}

func (a *App) handleThumb(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !safeName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")

	// Some valid images cannot be decoded by the standard library, so no
	// thumbnail exists for them. Serving the original keeps the grid intact --
	// browsers decode what Go could not.
	path := a.photos.ThumbPath(name)
	if _, err := os.Stat(path); err != nil {
		path = a.photos.Path(name)
	}
	http.ServeFile(w, r, path)
}

// --- export -----------------------------------------------------------------

func (a *App) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListItems(Query{Sort: "name"})
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="inventory.csv"`)

	cw := csv.NewWriter(w)
	defer cw.Flush()
	cw.Write([]string{"id", "name", "category", "quantity", "location", "part_number",
		"value", "tags", "link", "notes", "folder", "photos", "updated_at"})
	for _, it := range items {
		cw.Write([]string{
			strconv.FormatInt(it.ID, 10), it.Name, it.Category, strconv.Itoa(it.Quantity),
			it.Location, it.PartNumber, it.Value, it.TagString(), it.Link, it.Notes,
			it.FolderName,
			strconv.Itoa(len(it.Photos)), it.UpdatedAt.Format("2006-01-02 15:04"),
		})
	}
}

// --- auth pages -------------------------------------------------------------

func (a *App) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if !a.auth.Enabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.render(w, r, "login.html", "Sign in", map[string]string{"Next": safeNext(r.URL.Query().Get("next"))})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !a.auth.Enabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !a.auth.Check(r.FormValue("password")) {
		redirect(w, r, "/login", "", "Wrong password")
		return
	}
	a.auth.issue(w, r)
	http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.auth.clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// safeNext keeps the post-login redirect on this site: only same-origin,
// absolute paths are allowed through.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

// --- helpers ----------------------------------------------------------------

// parseForm reads a submission whether or not it carries file uploads, so a
// plain urlencoded POST (curl, a script, a form with no photo picker) is still
// validated normally instead of failing at the parser.
func parseForm(r *http.Request) error {
	err := r.ParseMultipartForm(8 << 20)
	if err == nil || errors.Is(err, http.ErrNotMultipart) {
		return nil
	}
	return err
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

func (a *App) lookup(w http.ResponseWriter, r *http.Request) (Item, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return Item{}, false
	}
	it, err := a.store.GetItem(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return Item{}, false
	}
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return Item{}, false
	}
	return it, true
}
