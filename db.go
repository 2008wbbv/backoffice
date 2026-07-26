package main

import (
	"database/sql"
	"fmt"
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
	Tags       string
	Link       string
	Notes      string
	CreatedAt  time.Time
	UpdatedAt  time.Time

	Photos []Photo
}

// Thumb is the filename of the cover image, or "" when the item has no photo.
func (i Item) Thumb() string {
	if len(i.Photos) == 0 {
		return ""
	}
	return i.Photos[0].Filename
}

func (i Item) TagList() []string {
	var out []string
	for _, t := range strings.Split(i.Tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

type Photo struct {
	ID       int64
	ItemID   int64
	Filename string
	Position int
}

const schema = `
CREATE TABLE IF NOT EXISTS items (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT    NOT NULL,
	category    TEXT    NOT NULL DEFAULT '',
	quantity    INTEGER NOT NULL DEFAULT 0,
	location    TEXT    NOT NULL DEFAULT '',
	part_number TEXT    NOT NULL DEFAULT '',
	value       TEXT    NOT NULL DEFAULT '',
	tags        TEXT    NOT NULL DEFAULT '',
	link        TEXT    NOT NULL DEFAULT '',
	notes       TEXT    NOT NULL DEFAULT '',
	created_at  TEXT    NOT NULL,
	updated_at  TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS photos (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	item_id  INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	filename TEXT    NOT NULL,
	position INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_photos_item ON photos(item_id, position);
CREATE INDEX IF NOT EXISTS idx_items_category ON items(category);
CREATE INDEX IF NOT EXISTS idx_items_location ON items(location);
`

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
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

const itemCols = `id, name, category, quantity, location, part_number, value, tags, link, notes, created_at, updated_at`

func scanItem(s interface{ Scan(...any) error }) (Item, error) {
	var it Item
	var created, updated string
	err := s.Scan(&it.ID, &it.Name, &it.Category, &it.Quantity, &it.Location,
		&it.PartNumber, &it.Value, &it.Tags, &it.Link, &it.Notes, &created, &updated)
	if err != nil {
		return it, err
	}
	it.CreatedAt, _ = time.Parse(time.RFC3339, created)
	it.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return it, nil
}

// Query describes the filter state of the grid page. Every field is optional.
type Query struct {
	Search   string
	Category string
	Location string
	Sort     string // "recent" (default), "name", "qty", "low"
}

func (s *Store) ListItems(q Query) ([]Item, error) {
	where := []string{"1=1"}
	var args []any

	if term := strings.TrimSpace(q.Search); term != "" {
		// Every word must match somewhere in the item. Typing "esp32 drawer"
		// finds ESP32s stored in a drawer.
		for _, word := range strings.Fields(term) {
			// ESCAPE '\' is required for escapeLike's backslashes to be read as
			// escapes; without it a search for "%" matches every row.
			where = append(where, `(name LIKE ?1 ESCAPE '\' OR category LIKE ?1 ESCAPE '\'
				OR location LIKE ?1 ESCAPE '\' OR part_number LIKE ?1 ESCAPE '\'
				OR value LIKE ?1 ESCAPE '\' OR tags LIKE ?1 ESCAPE '\'
				OR notes LIKE ?1 ESCAPE '\')`)
			args = append(args, "%"+escapeLike(word)+"%")
		}
	}
	if q.Category != "" {
		where = append(where, "category = ?")
		args = append(args, q.Category)
	}
	if q.Location != "" {
		where = append(where, "location = ?")
		args = append(args, q.Location)
	}

	order := "updated_at DESC"
	switch q.Sort {
	case "name":
		order = "name COLLATE NOCASE ASC"
	case "qty":
		order = "quantity DESC, name COLLATE NOCASE ASC"
	case "low":
		order = "quantity ASC, name COLLATE NOCASE ASC"
	}

	// The ?1 placeholders above are per-word, so rebuild the arg list
	// positionally: rewrite each clause with plain ? and repeat the arg.
	sqlWhere, sqlArgs := expandNumbered(where, args)

	rows, err := s.db.Query(
		`SELECT `+itemCols+` FROM items WHERE `+sqlWhere+` ORDER BY `+order, sqlArgs...)
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

	// One extra query attaches every photo, instead of N queries in the grid.
	prows, err := s.db.Query(`SELECT id, item_id, filename, position FROM photos ORDER BY item_id, position, id`)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var p Photo
		if err := prows.Scan(&p.ID, &p.ItemID, &p.Filename, &p.Position); err != nil {
			return nil, err
		}
		if idx, ok := byID[p.ItemID]; ok {
			items[idx].Photos = append(items[idx].Photos, p)
		}
	}
	return items, prows.Err()
}

func (s *Store) GetItem(id int64) (Item, error) {
	row := s.db.QueryRow(`SELECT `+itemCols+` FROM items WHERE id = ?`, id)
	it, err := scanItem(row)
	if err != nil {
		return it, err
	}
	rows, err := s.db.Query(`SELECT id, item_id, filename, position FROM photos WHERE item_id = ? ORDER BY position, id`, id)
	if err != nil {
		return it, err
	}
	defer rows.Close()
	for rows.Next() {
		var p Photo
		if err := rows.Scan(&p.ID, &p.ItemID, &p.Filename, &p.Position); err != nil {
			return it, err
		}
		it.Photos = append(it.Photos, p)
	}
	return it, rows.Err()
}

func (s *Store) CreateItem(it Item) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(`INSERT INTO items
		(name, category, quantity, location, part_number, value, tags, link, notes, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		it.Name, it.Category, it.Quantity, it.Location, it.PartNumber,
		it.Value, it.Tags, it.Link, it.Notes, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateItem(it Item) error {
	_, err := s.db.Exec(`UPDATE items SET
		name=?, category=?, quantity=?, location=?, part_number=?,
		value=?, tags=?, link=?, notes=?, updated_at=?
		WHERE id=?`,
		it.Name, it.Category, it.Quantity, it.Location, it.PartNumber,
		it.Value, it.Tags, it.Link, it.Notes,
		time.Now().UTC().Format(time.RFC3339), it.ID)
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

// DeleteItem removes the row and returns the photo filenames that are now
// orphaned, so the caller can unlink them from disk.
func (s *Store) DeleteItem(id int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT filename FROM photos WHERE item_id = ?`, id)
	if err != nil {
		return nil, err
	}
	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			rows.Close()
			return nil, err
		}
		files = append(files, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`DELETE FROM items WHERE id = ?`, id)
	return files, err
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

// Facet is one value of a filterable field plus how many items use it. These
// drive the category and location chips, which is why locations never need to
// be configured up front -- they are just whatever you have typed so far.
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
	rows, err := s.db.Query(`SELECT ` + column + `, COUNT(*) FROM items
		WHERE ` + column + ` <> '' GROUP BY ` + column + ` ORDER BY ` + column + ` COLLATE NOCASE`)
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
	Items  int
	Pieces int
	Photos int
}

func (s *Store) Stats() (Stats, error) {
	var st Stats
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(quantity),0) FROM items`).Scan(&st.Items, &st.Pieces)
	if err != nil {
		return st, err
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM photos`).Scan(&st.Photos)
	return st, err
}

type Store struct{ db *sql.DB }

func escapeLike(s string) string {
	r := strings.NewReplacer("%", `\%`, "_", `\_`, `\`, `\\`)
	return r.Replace(s)
}

// expandNumbered rewrites clauses that use ?1 into plain ? placeholders,
// repeating the matching argument once per occurrence.
func expandNumbered(clauses []string, args []any) (string, []any) {
	var out []any
	argi := 0
	for i, c := range clauses {
		n := strings.Count(c, "?1")
		if n > 0 {
			for j := 0; j < n; j++ {
				out = append(out, args[argi])
			}
			clauses[i] = strings.ReplaceAll(c, "?1", "?")
			argi++
			continue
		}
		if strings.Contains(c, "?") {
			out = append(out, args[argi])
			argi++
		}
	}
	return strings.Join(clauses, " AND "), out
}
