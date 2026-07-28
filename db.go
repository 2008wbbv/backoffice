package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Item is a single kind of part on a shelf: a reel of resistors, a tray of
// ESP32s, one specific SBC. Quantity lives on the item, not on a separate
// stock table -- a homelab does not need lot tracking.
type Item struct {
	ID         int64
	Name       string
	Category   string
	Quantity   int
	Location   string
	PartNumber string
	Value      string
	Link       string
	Notes      string
	FolderID   *int64
	CreatedAt  time.Time
	UpdatedAt  time.Time

	FolderName string
	Tags       []string
	Photos     []Photo
}

// Thumb is the filename of the cover image, or "" when the item has no photo.
func (i Item) Thumb() string {
	if len(i.Photos) == 0 {
		return ""
	}
	return i.Photos[0].Filename
}

func (i Item) TagString() string { return strings.Join(i.Tags, ", ") }

// FolderRef and InFolder exist because html/template cannot dereference or
// compare a *int64, and folder membership is optional.
func (i Item) FolderRef() int64 {
	if i.FolderID == nil {
		return 0
	}
	return *i.FolderID
}

func (i Item) InFolder(id int64) bool { return i.FolderID != nil && *i.FolderID == id }

type Photo struct {
	ID       int64
	ItemID   int64
	Filename string
	Position int
}

// Folder is a user-made grouping shown on the dashboard -- a project, a bench,
// a parts cabinet. Folders nest, and are deliberately separate from location
// (where a thing physically is) and type (what a thing is).
type Folder struct {
	ID       int64
	Name     string
	ParentID *int64
	Depth    int

	// Direct contents.
	ItemCount int
	// Contents including every descendant folder, which is what the dashboard
	// card shows.
	TotalItems  int
	TotalPieces int
	Cover       string
	Children    []*Folder
}

type Store struct{ db *sql.DB }

func openDB(path string) (*sql.DB, error) {
	// _pragma args are how modernc.org/sqlite takes PRAGMAs. WAL keeps the UI
	// responsive while a photo upload is committing.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc's driver is safe for concurrent use, but a single writer avoids
	// SQLITE_BUSY churn entirely on a box this small.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	return db, nil
}

const itemCols = `i.id, i.name, i.category, i.quantity, i.location, i.part_number,
	i.value, i.link, i.notes, i.folder_id, i.created_at, i.updated_at,
	COALESCE(f.name, '')`

func scanItem(s interface{ Scan(...any) error }) (Item, error) {
	var it Item
	var created, updated string
	err := s.Scan(&it.ID, &it.Name, &it.Category, &it.Quantity, &it.Location,
		&it.PartNumber, &it.Value, &it.Link, &it.Notes, &it.FolderID,
		&created, &updated, &it.FolderName)
	if err != nil {
		return it, err
	}
	it.CreatedAt, _ = time.Parse(time.RFC3339, created)
	it.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return it, nil
}

// Query describes the filter state of the item grid. Every field is optional.
type Query struct {
	Search    string
	Category  string
	Location  string
	Tags      []string // an item must carry all of these
	FolderID  *int64   // limit to one folder (with Recursive, its subtree too)
	Unfiled   bool     // only items in no folder
	Recursive bool
	Sort      string // "recent" (default), "name", "qty", "low"
}

func (q Query) Any() bool {
	return q.Search != "" || q.Category != "" || q.Location != "" ||
		len(q.Tags) > 0 || q.FolderID != nil || q.Unfiled
}

// values renders the query as URL parameters. Folder scope is carried in the
// path rather than here, so it is deliberately omitted.
func (q Query) values() url.Values {
	v := url.Values{}
	if q.Search != "" {
		v.Set("q", q.Search)
	}
	if q.Category != "" {
		v.Set("category", q.Category)
	}
	if q.Location != "" {
		v.Set("location", q.Location)
	}
	if q.Sort != "" && q.Sort != "recent" {
		v.Set("sort", q.Sort)
	}
	if q.Unfiled {
		v.Set("unfiled", "1")
	}
	for _, t := range q.Tags {
		v.Add("tag", t)
	}
	return v
}

// Path is the page this query belongs on: a folder's own view when scoped to
// one, otherwise the all-items grid.
func (q Query) Path() string {
	if q.FolderID != nil {
		return fmt.Sprintf("/folders/%d", *q.FolderID)
	}
	return "/items"
}

func (q Query) String() string {
	if v := q.values(); len(v) > 0 {
		return q.Path() + "?" + v.Encode()
	}
	return q.Path()
}

// URL returns this query with one parameter replaced. Setting a parameter to
// the value it already has clears it instead, so chips toggle.
func (q Query) URL(key, value string) string {
	v := q.values()
	if v.Get(key) == value && key != "sort" {
		v.Del(key)
	} else if value == "" {
		v.Del(key)
	} else {
		v.Set(key, value)
	}
	if len(v) == 0 {
		return q.Path()
	}
	return q.Path() + "?" + v.Encode()
}

// WithTagToggled adds a tag to the filter, or drops it if already applied.
func (q Query) WithTagToggled(tag string) string {
	v := q.values()
	v.Del("tag")
	found := false
	for _, t := range q.Tags {
		if strings.EqualFold(t, tag) {
			found = true
			continue
		}
		v.Add("tag", t)
	}
	if !found {
		v.Add("tag", tag)
	}
	if len(v) == 0 {
		return q.Path()
	}
	return q.Path() + "?" + v.Encode()
}

func (s *Store) ListItems(q Query) ([]Item, error) {
	var where []string
	var args []any

	if term := strings.TrimSpace(q.Search); term != "" {
		// Every word must match somewhere in the item, so typing
		// "esp32 drawer" finds ESP32s stored in a drawer.
		for _, word := range strings.Fields(term) {
			// ESCAPE '\' is required for escapeLike's backslashes to be read as
			// escapes; without it a search for "%" matches every row.
			const field = ` LIKE ? ESCAPE '\' OR `
			where = append(where, `(i.name`+field+`i.category`+field+`i.location`+field+
				`i.part_number`+field+`i.value`+field+`i.notes LIKE ? ESCAPE '\'
				OR EXISTS (SELECT 1 FROM item_tags it JOIN tags t ON t.id = it.tag_id
					WHERE it.item_id = i.id AND t.name LIKE ? ESCAPE '\'))`)
			pattern := "%" + escapeLike(word) + "%"
			for n := 0; n < 7; n++ {
				args = append(args, pattern)
			}
		}
	}
	if q.Category != "" {
		where = append(where, "i.category = ?")
		args = append(args, q.Category)
	}
	if q.Location != "" {
		where = append(where, "i.location = ?")
		args = append(args, q.Location)
	}
	// Each tag gets its own EXISTS so multiple tags narrow rather than widen.
	for _, tag := range q.Tags {
		where = append(where, `EXISTS (SELECT 1 FROM item_tags it JOIN tags t ON t.id = it.tag_id
			WHERE it.item_id = i.id AND t.name = ?)`)
		args = append(args, tag)
	}
	switch {
	case q.Unfiled:
		where = append(where, "i.folder_id IS NULL")
	case q.FolderID != nil && q.Recursive:
		where = append(where, `i.folder_id IN (
			WITH RECURSIVE subtree(id) AS (
				SELECT ? UNION ALL
				SELECT f.id FROM folders f JOIN subtree s ON f.parent_id = s.id
			) SELECT id FROM subtree)`)
		args = append(args, *q.FolderID)
	case q.FolderID != nil:
		where = append(where, "i.folder_id = ?")
		args = append(args, *q.FolderID)
	}

	order := "i.updated_at DESC"
	switch q.Sort {
	case "name":
		order = "i.name COLLATE NOCASE ASC"
	case "qty":
		order = "i.quantity DESC, i.name COLLATE NOCASE ASC"
	case "low":
		order = "i.quantity ASC, i.name COLLATE NOCASE ASC"
	}

	clause := "1=1"
	if len(where) > 0 {
		clause = strings.Join(where, " AND ")
	}
	rows, err := s.db.Query(`SELECT `+itemCols+` FROM items i
		LEFT JOIN folders f ON f.id = i.folder_id
		WHERE `+clause+` ORDER BY `+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Item
	byID := map[int64]int{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		byID[it.ID] = len(items)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return items, nil
	}
	if err := s.attachPhotos(items, byID); err != nil {
		return nil, err
	}
	return items, s.attachTags(items, byID)
}

// attachPhotos and attachTags each cost one query for the whole page, rather
// than two per item in the grid.
func (s *Store) attachPhotos(items []Item, byID map[int64]int) error {
	rows, err := s.db.Query(`SELECT id, item_id, filename, position FROM photos
		ORDER BY item_id, position, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p Photo
		if err := rows.Scan(&p.ID, &p.ItemID, &p.Filename, &p.Position); err != nil {
			return err
		}
		if idx, ok := byID[p.ItemID]; ok {
			items[idx].Photos = append(items[idx].Photos, p)
		}
	}
	return rows.Err()
}

func (s *Store) attachTags(items []Item, byID map[int64]int) error {
	rows, err := s.db.Query(`SELECT it.item_id, t.name FROM item_tags it
		JOIN tags t ON t.id = it.tag_id ORDER BY t.name COLLATE NOCASE`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var itemID int64
		var name string
		if err := rows.Scan(&itemID, &name); err != nil {
			return err
		}
		if idx, ok := byID[itemID]; ok {
			items[idx].Tags = append(items[idx].Tags, name)
		}
	}
	return rows.Err()
}

func (s *Store) GetItem(id int64) (Item, error) {
	row := s.db.QueryRow(`SELECT `+itemCols+` FROM items i
		LEFT JOIN folders f ON f.id = i.folder_id WHERE i.id = ?`, id)
	it, err := scanItem(row)
	if err != nil {
		return it, err
	}
	items := []Item{it}
	byID := map[int64]int{it.ID: 0}
	if err := s.attachPhotos(items, byID); err != nil {
		return it, err
	}
	if err := s.attachTags(items, byID); err != nil {
		return it, err
	}
	return items[0], nil
}

func (s *Store) CreateItem(it Item) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.Exec(`INSERT INTO items
		(name, category, quantity, location, part_number, value, link, notes,
		 folder_id, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		it.Name, it.Category, it.Quantity, it.Location, it.PartNumber,
		it.Value, it.Link, it.Notes, it.FolderID, now, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := setTagsTx(tx, id, it.Tags); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) UpdateItem(it Item) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`UPDATE items SET
		name=?, category=?, quantity=?, location=?, part_number=?,
		value=?, link=?, notes=?, folder_id=?, updated_at=?
		WHERE id=?`,
		it.Name, it.Category, it.Quantity, it.Location, it.PartNumber,
		it.Value, it.Link, it.Notes, it.FolderID,
		time.Now().UTC().Format(time.RFC3339), it.ID)
	if err != nil {
		return err
	}
	if err := setTagsTx(tx, it.ID, it.Tags); err != nil {
		return err
	}
	return tx.Commit()
}

// setTagsTx replaces an item's tags wholesale, then drops any tag row that no
// longer has items so the tag filter never lists dead tags.
func setTagsTx(tx *sql.Tx, itemID int64, tags []string) error {
	if _, err := tx.Exec(`DELETE FROM item_tags WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	for _, name := range tags {
		if err := attachTagTx(tx, itemID, name); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`DELETE FROM tags WHERE id NOT IN (SELECT tag_id FROM item_tags)`)
	return err
}

// AdjustQuantity applies a relative change and returns the clamped result, so
// the +/- buttons in the grid never need to read-modify-write from the client.
func (s *Store) AdjustQuantity(id int64, delta int) (int, error) {
	_, err := s.db.Exec(`UPDATE items SET quantity = MAX(0, quantity + ?), updated_at = ? WHERE id = ?`,
		delta, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return 0, err
	}
	var q int
	err = s.db.QueryRow(`SELECT quantity FROM items WHERE id = ?`, id).Scan(&q)
	return q, err
}

func (s *Store) MoveItem(id int64, folderID *int64) error {
	_, err := s.db.Exec(`UPDATE items SET folder_id = ?, updated_at = ? WHERE id = ?`,
		folderID, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// DeleteItem removes the row and returns the photo filenames that are now
// orphaned, so the caller can unlink them from disk.
func (s *Store) DeleteItem(id int64) ([]string, error) {
	files, err := s.photoFilenames(`SELECT filename FROM photos WHERE item_id = ?`, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`DELETE FROM items WHERE id = ?`, id); err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`DELETE FROM tags WHERE id NOT IN (SELECT tag_id FROM item_tags)`)
	return files, err
}

func (s *Store) photoFilenames(query string, args ...any) ([]string, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

func (s *Store) AddPhoto(itemID int64, filename string) error {
	var next int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(position)+1, 0) FROM photos WHERE item_id = ?`, itemID).Scan(&next)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO photos (item_id, filename, position) VALUES (?,?,?)`, itemID, filename, next)
	return err
}

func (s *Store) DeletePhoto(photoID int64) (itemID int64, filename string, err error) {
	err = s.db.QueryRow(`SELECT item_id, filename FROM photos WHERE id = ?`, photoID).Scan(&itemID, &filename)
	if err != nil {
		return 0, "", err
	}
	_, err = s.db.Exec(`DELETE FROM photos WHERE id = ?`, photoID)
	return itemID, filename, err
}

// SetCoverPhoto moves one photo to the front of an item's photo list.
func (s *Store) SetCoverPhoto(photoID int64) (int64, error) {
	var itemID int64
	if err := s.db.QueryRow(`SELECT item_id FROM photos WHERE id = ?`, photoID).Scan(&itemID); err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`UPDATE photos SET position = position + 1 WHERE item_id = ?`, itemID); err != nil {
		return itemID, err
	}
	_, err := s.db.Exec(`UPDATE photos SET position = 0 WHERE id = ?`, photoID)
	return itemID, err
}

// --- folders ----------------------------------------------------------------

func (s *Store) CreateFolder(name string, parentID *int64) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO folders (name, parent_id, created_at) VALUES (?,?,?)`,
		name, parentID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetFolder(id int64) (Folder, error) {
	var f Folder
	err := s.db.QueryRow(`SELECT id, name, parent_id FROM folders WHERE id = ?`, id).
		Scan(&f.ID, &f.Name, &f.ParentID)
	return f, err
}

// RenameFolder also handles reparenting. It refuses to move a folder inside its
// own subtree, which would orphan the whole branch from the dashboard.
func (s *Store) RenameFolder(id int64, name string, parentID *int64) error {
	if parentID != nil {
		if *parentID == id {
			return fmt.Errorf("a folder cannot contain itself")
		}
		inside, err := s.isDescendant(*parentID, id)
		if err != nil {
			return err
		}
		if inside {
			return fmt.Errorf("cannot move a folder into one of its own subfolders")
		}
	}
	_, err := s.db.Exec(`UPDATE folders SET name = ?, parent_id = ? WHERE id = ?`, name, parentID, id)
	return err
}

// isDescendant reports whether candidate sits anywhere under root.
func (s *Store) isDescendant(candidate, root int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`WITH RECURSIVE subtree(id) AS (
			SELECT ? UNION ALL
			SELECT f.id FROM folders f JOIN subtree s ON f.parent_id = s.id
		) SELECT COUNT(*) FROM subtree WHERE id = ?`, root, candidate).Scan(&n)
	return n > 0, err
}

// DeleteFolder removes a folder and its subfolders. Items are never deleted --
// they just become unfiled, which is why this needs no photo cleanup.
func (s *Store) DeleteFolder(id int64) error {
	_, err := s.db.Exec(`DELETE FROM folders WHERE id = ?`, id)
	return err
}

// Ancestors returns the path from the root down to and including id, for
// breadcrumbs.
func (s *Store) Ancestors(id int64) ([]Folder, error) {
	rows, err := s.db.Query(`WITH RECURSIVE up(id, name, parent_id, depth) AS (
			SELECT id, name, parent_id, 0 FROM folders WHERE id = ?
			UNION ALL
			SELECT f.id, f.name, f.parent_id, up.depth + 1
			FROM folders f JOIN up ON f.id = up.parent_id
		) SELECT id, name, parent_id FROM up ORDER BY depth DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.Name, &f.ParentID); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FolderTree loads every folder with its rolled-up counts and a cover photo
// borrowed from something inside it. Counts are rolled up in Go rather than in
// a recursive query per folder: one pass over a homelab's worth of folders is
// cheaper and much easier to follow.
func (s *Store) FolderTree() ([]*Folder, error) {
	all, err := s.flatFolders()
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*Folder, len(all))
	for _, f := range all {
		byID[f.ID] = f
	}

	// Direct item and piece counts per folder.
	rows, err := s.db.Query(`SELECT folder_id, COUNT(*), COALESCE(SUM(quantity), 0)
		FROM items WHERE folder_id IS NOT NULL GROUP BY folder_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var items, pieces int
		if err := rows.Scan(&id, &items, &pieces); err != nil {
			rows.Close()
			return nil, err
		}
		if f, ok := byID[id]; ok {
			f.ItemCount, f.TotalItems, f.TotalPieces = items, items, pieces
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// One cover photo per folder, taken from that folder's own items.
	covers, err := s.db.Query(`SELECT i.folder_id, p.filename FROM photos p
		JOIN items i ON i.id = p.item_id
		WHERE i.folder_id IS NOT NULL
		ORDER BY i.folder_id, p.position, p.id`)
	if err != nil {
		return nil, err
	}
	for covers.Next() {
		var id int64
		var file string
		if err := covers.Scan(&id, &file); err != nil {
			covers.Close()
			return nil, err
		}
		if f, ok := byID[id]; ok && f.Cover == "" {
			f.Cover = file
		}
	}
	covers.Close()
	if err := covers.Err(); err != nil {
		return nil, err
	}

	// Link children to parents, dropping any whose parent vanished.
	var roots []*Folder
	for _, f := range all {
		if f.ParentID == nil {
			roots = append(roots, f)
			continue
		}
		if parent, ok := byID[*f.ParentID]; ok {
			parent.Children = append(parent.Children, f)
		} else {
			roots = append(roots, f)
		}
	}
	for _, r := range roots {
		rollUp(r, 0)
	}
	sortFolders(roots)
	return roots, nil
}

func (s *Store) flatFolders() ([]*Folder, error) {
	rows, err := s.db.Query(`SELECT id, name, parent_id FROM folders ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []*Folder
	for rows.Next() {
		f := &Folder{}
		if err := rows.Scan(&f.ID, &f.Name, &f.ParentID); err != nil {
			return nil, err
		}
		all = append(all, f)
	}
	return all, rows.Err()
}

// rollUp adds each folder's descendants into its totals and records its depth.
func rollUp(f *Folder, depth int) {
	f.Depth = depth
	for _, c := range f.Children {
		rollUp(c, depth+1)
		f.TotalItems += c.TotalItems
		f.TotalPieces += c.TotalPieces
		if f.Cover == "" {
			f.Cover = c.Cover
		}
	}
}

func sortFolders(fs []*Folder) {
	sort.SliceStable(fs, func(i, j int) bool {
		return strings.ToLower(fs[i].Name) < strings.ToLower(fs[j].Name)
	})
	for _, f := range fs {
		sortFolders(f.Children)
	}
}

// FlattenFolders turns the tree into a depth-ordered list, for <select> menus
// and the sidebar.
func FlattenFolders(roots []*Folder) []*Folder {
	var out []*Folder
	var walk func([]*Folder)
	walk = func(fs []*Folder) {
		for _, f := range fs {
			out = append(out, f)
			walk(f.Children)
		}
	}
	walk(roots)
	return out
}

// Indent renders a folder's nesting for plain-text contexts like <option>.
func (f *Folder) Indent() string { return strings.Repeat("— ", f.Depth) }

func (f Folder) HasParent(id int64) bool { return f.ParentID != nil && *f.ParentID == id }

// --- facets and stats -------------------------------------------------------

// Facet is one value of a filterable field plus how many items use it. These
// drive the type, location and tag chips, which is why none of them need to be
// configured up front -- they are just whatever you have typed so far.
type Facet struct {
	Value string
	Count int
}

func (s *Store) Facets(column string) ([]Facet, error) {
	switch column {
	case "category", "location": // guard: column is interpolated below
	default:
		return nil, fmt.Errorf("facet: unsupported column %q", column)
	}
	return s.facetQuery(`SELECT ` + column + `, COUNT(*) FROM items
		WHERE ` + column + ` <> '' GROUP BY ` + column + ` ORDER BY ` + column + ` COLLATE NOCASE`)
}

func (s *Store) TagFacets() ([]Facet, error) {
	return s.facetQuery(`SELECT t.name, COUNT(*) FROM tags t
		JOIN item_tags it ON it.tag_id = t.id
		GROUP BY t.id ORDER BY COUNT(*) DESC, t.name COLLATE NOCASE`)
}

func (s *Store) facetQuery(query string, args ...any) ([]Facet, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Facet
	for rows.Next() {
		var f Facet
		if err := rows.Scan(&f.Value, &f.Count); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

type Stats struct {
	Items   int
	Pieces  int
	Photos  int
	Folders int
	Tags    int
	Unfiled int
	OutOf   int // items at zero quantity
}

func (s *Store) Stats() (Stats, error) {
	var st Stats
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(quantity),0),
		COALESCE(SUM(CASE WHEN quantity = 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN folder_id IS NULL THEN 1 ELSE 0 END), 0)
		FROM items`).Scan(&st.Items, &st.Pieces, &st.OutOf, &st.Unfiled)
	if err != nil {
		return st, err
	}
	err = s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM photos),
		(SELECT COUNT(*) FROM folders),
		(SELECT COUNT(*) FROM tags)`).Scan(&st.Photos, &st.Folders, &st.Tags)
	return st, err
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(s)
}
