package main

import (
	"database/sql"
	"fmt"
	"strings"
)

// migration is one forward step. Most are plain SQL; a few need Go to reshape
// existing rows. Steps run in order inside a transaction and the database's
// user_version records how far it has got, so an old data directory upgrades
// itself on the next start and there is still no migration command to run.
type migration struct {
	name string
	sql  string
	fn   func(*sql.Tx) error
}

var migrations = []migration{
	{
		name: "initial schema",
		sql: `
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
`,
	},
	{
		name: "folders",
		sql: `
CREATE TABLE folders (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT    NOT NULL,
	parent_id  INTEGER REFERENCES folders(id) ON DELETE CASCADE,
	created_at TEXT    NOT NULL
);

CREATE INDEX idx_folders_parent ON folders(parent_id);

-- Deleting a folder leaves its items in place, just unfiled.
ALTER TABLE items ADD COLUMN folder_id INTEGER REFERENCES folders(id) ON DELETE SET NULL;
CREATE INDEX idx_items_folder ON items(folder_id);
`,
	},
	{
		name: "normalised tags",
		sql: `
CREATE TABLE tags (
	id   INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL COLLATE NOCASE UNIQUE
);

CREATE TABLE item_tags (
	item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	tag_id  INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
	PRIMARY KEY (item_id, tag_id)
);

CREATE INDEX idx_item_tags_tag ON item_tags(tag_id);
`,
		// Split the old comma-separated items.tags column into the new tables so
		// tags people already typed survive the upgrade.
		fn: func(tx *sql.Tx) error {
			rows, err := tx.Query(`SELECT id, tags FROM items WHERE tags <> ''`)
			if err != nil {
				return err
			}
			existing := map[int64]string{}
			for rows.Next() {
				var id int64
				var tags string
				if err := rows.Scan(&id, &tags); err != nil {
					rows.Close()
					return err
				}
				existing[id] = tags
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}

			// This insert is deliberately written against the tags table as it
			// exists at *this* step, rather than calling the shared helper.
			// A migration has to keep working against the schema of its own
			// moment: the helper later learned to write an icon column that
			// does not exist until the next migration.
			link := func(itemID int64, name string) error {
				var id int64
				err := tx.QueryRow(`SELECT id FROM tags WHERE name = ?`, name).Scan(&id)
				if err == sql.ErrNoRows {
					res, err := tx.Exec(`INSERT INTO tags (name) VALUES (?)`, name)
					if err != nil {
						return err
					}
					if id, err = res.LastInsertId(); err != nil {
						return err
					}
				} else if err != nil {
					return err
				}
				_, err = tx.Exec(`INSERT OR IGNORE INTO item_tags (item_id, tag_id) VALUES (?, ?)`, itemID, id)
				return err
			}

			for itemID, raw := range existing {
				for _, name := range splitTags(raw) {
					if err := link(itemID, name); err != nil {
						return err
					}
				}
			}
			// The column is now a stale duplicate of item_tags.
			_, err = tx.Exec(`ALTER TABLE items DROP COLUMN tags`)
			return err
		},
	},
	{
		name: "tag icons",
		sql:  `ALTER TABLE tags ADD COLUMN icon TEXT NOT NULL DEFAULT '';`,
		// Give the tags that already exist an icon, so the upgrade is visible
		// rather than leaving a wall of blank tags.
		fn: func(tx *sql.Tx) error {
			rows, err := tx.Query(`SELECT id, name FROM tags`)
			if err != nil {
				return err
			}
			icons := map[int64]string{}
			for rows.Next() {
				var id int64
				var name string
				if err := rows.Scan(&id, &name); err != nil {
					rows.Close()
					return err
				}
				icons[id] = IconForTag(name)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for id, icon := range icons {
				if _, err := tx.Exec(`UPDATE tags SET icon = ? WHERE id = ?`, icon, id); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		name: "prices per source",
		sql: `
CREATE TABLE prices (
	item_id    INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	source     TEXT    NOT NULL,
	amount     REAL    NOT NULL,
	currency   TEXT    NOT NULL DEFAULT 'USD',
	url        TEXT    NOT NULL DEFAULT '',
	updated_at TEXT    NOT NULL,
	PRIMARY KEY (item_id, source)
);
`,
	},
	{
		name: "interfaces, specs and references",
		sql: `
-- What a part speaks and needs: I2C, SPI, 3V3 logic, USB-C, and so on. A
-- controlled vocabulary rather than free tags, so a project can reason about it.
CREATE TABLE item_interfaces (
	item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	name    TEXT    NOT NULL,
	PRIMARY KEY (item_id, name)
);
CREATE INDEX idx_item_interfaces_name ON item_interfaces(name);

-- Free-form key/value specifications: "Logic level = 3.3V", "Flash = 8MB".
CREATE TABLE specs (
	item_id  INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	name     TEXT    NOT NULL,
	value    TEXT    NOT NULL,
	position INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (item_id, name)
);

-- Datasheets, pinout diagrams, manuals. A reference is a link, an stored
-- image, or both -- a pinout is only useful if you can actually look at it.
CREATE TABLE refs (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	item_id  INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	kind     TEXT    NOT NULL DEFAULT 'reference',
	title    TEXT    NOT NULL,
	url      TEXT    NOT NULL DEFAULT '',
	filename TEXT    NOT NULL DEFAULT '',
	position INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_refs_item ON refs(item_id, position);
`,
	},
	{
		name: "price history",
		sql: `
CREATE TABLE price_history (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	item_id  INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	source   TEXT    NOT NULL,
	amount   REAL    NOT NULL,
	currency TEXT    NOT NULL DEFAULT 'USD',
	seen_at  TEXT    NOT NULL
);
CREATE INDEX idx_price_history ON price_history(item_id, source, seen_at);
`,
		// Seed history from the prices already recorded, so the first chart is
		// not empty for anyone upgrading.
		fn: func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO price_history (item_id, source, amount, currency, seen_at)
				SELECT item_id, source, amount, currency, updated_at FROM prices`)
			return err
		},
	},
	{
		name: "projects",
		sql: `
CREATE TABLE projects (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT    NOT NULL,
	notes      TEXT    NOT NULL DEFAULT '',
	status     TEXT    NOT NULL DEFAULT 'planning',
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);

-- A line is either a part you own (item_id) or one you do not yet (name only),
-- which is what lets the shortfall list say "buy this".
CREATE TABLE project_parts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	item_id    INTEGER REFERENCES items(id) ON DELETE SET NULL,
	name       TEXT    NOT NULL DEFAULT '',
	quantity   INTEGER NOT NULL DEFAULT 1,
	note       TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_project_parts ON project_parts(project_id);
`,
	},
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database is at schema version %d but this build only knows %d; "+
			"it was written by a newer version of the app", version, len(migrations))
	}

	for i := version; i < len(migrations); i++ {
		m := migrations[i]
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if strings.TrimSpace(m.sql) != "" {
			if _, err := tx.Exec(m.sql); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d (%s): %w", i+1, m.name, err)
			}
		}
		if m.fn != nil {
			if err := m.fn(tx); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d (%s): %w", i+1, m.name, err)
			}
		}
		// PRAGMA user_version does not accept a bound parameter.
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d (%s): record version: %w", i+1, m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d (%s): commit: %w", i+1, m.name, err)
		}
	}
	return nil
}

// splitTags cleans a comma-separated tag string into distinct, trimmed names.
func splitTags(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t == "" || len(t) > 40 {
			continue
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

// attachTagTx links an item to a tag name, creating the tag if it is new.
// Tag names are compared case-insensitively, so "SMD" and "smd" are one tag.
func attachTagTx(tx *sql.Tx, itemID int64, name string) error {
	var id int64
	err := tx.QueryRow(`SELECT id FROM tags WHERE name = ?`, name).Scan(&id)
	if err == sql.ErrNoRows {
		res, err := tx.Exec(`INSERT INTO tags (name, icon) VALUES (?, ?)`, name, IconForTag(name))
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO item_tags (item_id, tag_id) VALUES (?, ?)`, itemID, id)
	return err
}
