package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// --- dashboard --------------------------------------------------------------

type dashboardData struct {
	Folders  []*Folder
	Flat     []*Folder
	Stats    Stats
	Recent   []Item
	LowStock []Item
	Tags     []Facet
	Unfiled  int
}

// handleDashboard is the landing page: folders first, then what changed and
// what is running out.
func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	tree, err := a.store.FolderTree()
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	stats, err := a.store.Stats()
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	recent, err := a.store.ListItems(Query{Sort: "recent"})
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	low, err := a.store.ListItems(Query{Sort: "low"})
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	tags, err := a.store.TagFacets()
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}

	a.render(w, r, "dashboard.html", "", dashboardData{
		Folders:  tree,
		Flat:     FlattenFolders(tree),
		Stats:    stats,
		Recent:   firstN(recent, 12),
		LowStock: lowStock(low, 8),
		Tags:     firstN(tags, 24),
		Unfiled:  stats.Unfiled,
	})
}

// firstN caps a slice for the dashboard panels.
func firstN[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// lowStock keeps only the genuinely low items -- an inventory where nothing is
// running out should show an empty panel, not its fullest shelves.
func lowStock(sortedAsc []Item, n int) []Item {
	var out []Item
	for _, it := range sortedAsc {
		if it.Quantity > 2 {
			break
		}
		out = append(out, it)
		if len(out) == n {
			break
		}
	}
	return out
}

// --- folder view ------------------------------------------------------------

type folderData struct {
	Folder      Folder
	Breadcrumb  []Folder
	Children    []*Folder
	AllFolders  []*Folder
	Items       []Item
	Categories  []Facet
	Locations   []Facet
	Tags        []Facet
	Stats       Stats
	Query       Query
	Recursive   bool
	TotalItems  int
	TotalPieces int
}

func (a *App) handleFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	folder, err := a.store.GetFolder(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}

	q := a.queryFromRequest(r)
	q.FolderID = &id
	q.Recursive = r.URL.Query().Get("all") == "1"

	items, err := a.store.ListItems(q)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	breadcrumb, err := a.store.Ancestors(id)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	tree, err := a.store.FolderTree()
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}

	// Pull this folder's node out of the tree for its rolled-up counts.
	var node *Folder
	for _, f := range FlattenFolders(tree) {
		if f.ID == id {
			node = f
			break
		}
	}
	data := folderData{
		Folder:     folder,
		Breadcrumb: breadcrumb,
		AllFolders: FlattenFolders(tree),
		Items:      items,
		Query:      q,
		Recursive:  q.Recursive,
	}
	if node != nil {
		data.Children = node.Children
		data.TotalItems = node.TotalItems
		data.TotalPieces = node.TotalPieces
	}
	data.Categories, _ = a.store.Facets("category")
	data.Locations, _ = a.store.Facets("location")
	data.Tags, _ = a.store.TagFacets()

	a.render(w, r, "folder.html", folder.Name, data)
}

func (a *App) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		a.fail(w, err, http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	back := safeNext(r.FormValue("return"))
	if name == "" {
		redirect(w, r, back, "", "a folder needs a name")
		return
	}
	parent := optionalID(r.FormValue("parent_id"))
	id, err := a.store.CreateFolder(name, parent)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	redirect(w, r, fmt.Sprintf("/folders/%d", id), "Created "+name, "")
}

func (a *App) handleRenameFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := parseForm(r); err != nil {
		a.fail(w, err, http.StatusBadRequest)
		return
	}
	dest := fmt.Sprintf("/folders/%d", id)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		redirect(w, r, dest, "", "a folder needs a name")
		return
	}
	if err := a.store.RenameFolder(id, name, optionalID(r.FormValue("parent_id"))); err != nil {
		redirect(w, r, dest, "", err.Error())
		return
	}
	redirect(w, r, dest, "Folder updated", "")
}

func (a *App) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	folder, err := a.store.GetFolder(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	if err := a.store.DeleteFolder(id); err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	dest := "/"
	if folder.ParentID != nil {
		dest = fmt.Sprintf("/folders/%d", *folder.ParentID)
	}
	redirect(w, r, dest, "Deleted folder "+folder.Name+"; its items are now unfiled", "")
}

// handleMoveItem is the one-click "file this somewhere" action from an item page.
func (a *App) handleMoveItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := parseForm(r); err != nil {
		a.fail(w, err, http.StatusBadRequest)
		return
	}
	if err := a.store.MoveItem(id, optionalID(r.FormValue("folder_id"))); err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	redirect(w, r, fmt.Sprintf("/items/%d", id), "Moved", "")
}

// optionalID reads a form field that may be blank, meaning "no folder".
func optionalID(raw string) *int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	return &id
}
