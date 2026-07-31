package main

import (
	"database/sql"
	"sort"
	"strings"
	"time"
)

// Interfaces is the controlled vocabulary of what a part speaks and needs.
// It is deliberately a fixed list rather than free tags: a project can only
// reason about compatibility if everyone spells I2C the same way.
var Interfaces = []struct{ Name, Icon, Group string }{
	{"I2C", "🔗", "Bus"},
	{"SPI", "🔗", "Bus"},
	{"UART", "🔗", "Bus"},
	{"1-Wire", "🧵", "Bus"},
	{"CAN", "🚌", "Bus"},
	{"I2S", "🎵", "Bus"},
	{"PWM", "〰", "Signal"},
	{"ADC", "📈", "Signal"},
	{"DAC", "📉", "Signal"},
	{"GPIO", "📌", "Signal"},
	{"USB-C", "🔌", "Power / host"},
	{"Micro-USB", "🔌", "Power / host"},
	{"USB-A", "🔌", "Power / host"},
	{"Barrel jack", "🔌", "Power / host"},
	{"JST-PH", "🔋", "Power / host"},
	{"Qwiic / STEMMA QT", "🔗", "Bus"},
	{"3V3 logic", "⚡", "Level"},
	{"5V logic", "⚡", "Level"},
	{"12V", "⚡", "Level"},
	{"Mains", "⚠", "Level"},
	{"WiFi", "📶", "Wireless"},
	{"Bluetooth / BLE", "🔵", "Wireless"},
	{"LoRa", "🛰", "Wireless"},
	{"Zigbee", "🐝", "Wireless"},
	{"Ethernet", "🌐", "Wireless"},
	{"HDMI", "🖵", "Display"},
	{"MIPI / DSI", "🖵", "Display"},
	{"microSD", "💾", "Storage"},
}

var interfaceIcons = func() map[string]string {
	m := make(map[string]string, len(Interfaces))
	for _, i := range Interfaces {
		m[i.Name] = i.Icon
	}
	return m
}()

// IconForInterface is used wherever an interface is rendered.
func IconForInterface(name string) string { return interfaceIcons[name] }

// InterfaceGroups returns the vocabulary grouped for the edit form's checkboxes.
func InterfaceGroups() []InterfaceGroup {
	var order []string
	byGroup := map[string][]string{}
	for _, i := range Interfaces {
		if _, seen := byGroup[i.Group]; !seen {
			order = append(order, i.Group)
		}
		byGroup[i.Group] = append(byGroup[i.Group], i.Name)
	}
	out := make([]InterfaceGroup, 0, len(order))
	for _, g := range order {
		out = append(out, InterfaceGroup{Name: g, Members: byGroup[g]})
	}
	return out
}

type InterfaceGroup struct {
	Name    string
	Members []string
}

// validInterface keeps typos and injected values out of the vocabulary.
func validInterface(name string) bool {
	_, ok := interfaceIcons[name]
	return ok
}

// Spec is one line of a part's specification table.
type Spec struct {
	Name  string
	Value string
}

// Reference is a datasheet, pinout diagram or manual. It can be a link, a
// stored image, or both -- a pinout is only useful if you can look at it.
type Reference struct {
	ID       int64
	ItemID   int64
	Kind     string
	Title    string
	URL      string
	Filename string
	Position int
}

func (r Reference) IsImage() bool { return r.Filename != "" }

// RefKinds are the reference types offered in the UI.
var RefKinds = []string{"datasheet", "pinout", "manual", "guide", "schematic", "reference"}

// --- interfaces -------------------------------------------------------------

func (s *Store) attachInterfaces(items []Item, byID map[int64]int) error {
	rows, err := s.db.Query(`SELECT item_id, name FROM item_interfaces ORDER BY name`)
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
			items[idx].Interfaces = append(items[idx].Interfaces, name)
		}
	}
	return rows.Err()
}

func setInterfacesTx(tx *sql.Tx, itemID int64, names []string) error {
	if _, err := tx.Exec(`DELETE FROM item_interfaces WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	for _, n := range names {
		if !validInterface(n) {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO item_interfaces (item_id, name) VALUES (?,?)`, itemID, n); err != nil {
			return err
		}
	}
	return nil
}

// InterfaceFacets counts how many items speak each interface, for the filter row.
func (s *Store) InterfaceFacets() ([]Facet, error) {
	rows, err := s.db.Query(`SELECT name, COUNT(*) FROM item_interfaces
		GROUP BY name ORDER BY COUNT(*) DESC, name`)
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
		f.Icon = IconForInterface(f.Value)
		out = append(out, f)
	}
	return out, rows.Err()
}

// --- specs ------------------------------------------------------------------

func (s *Store) attachSpecs(items []Item, byID map[int64]int) error {
	rows, err := s.db.Query(`SELECT item_id, name, value FROM specs ORDER BY item_id, position, name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var itemID int64
		var sp Spec
		if err := rows.Scan(&itemID, &sp.Name, &sp.Value); err != nil {
			return err
		}
		if idx, ok := byID[itemID]; ok {
			items[idx].Specs = append(items[idx].Specs, sp)
		}
	}
	return rows.Err()
}

func setSpecsTx(tx *sql.Tx, itemID int64, specs []Spec) error {
	if _, err := tx.Exec(`DELETE FROM specs WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	for i, sp := range specs {
		if sp.Name == "" {
			continue
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO specs (item_id, name, value, position)
			VALUES (?,?,?,?)`, itemID, sp.Name, sp.Value, i)
		if err != nil {
			return err
		}
	}
	return nil
}

// ParseSpecs reads the "name: value" lines the edit form collects.
func ParseSpecs(raw string) []Spec {
	var out []Spec
	seen := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			// A line with no colon is still worth keeping as a bare note.
			name, value = line, ""
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		out = append(out, Spec{Name: name, Value: value})
	}
	return out
}

// SpecText renders specs back into the textarea format.
func (i Item) SpecText() string {
	var b strings.Builder
	for _, sp := range i.Specs {
		b.WriteString(sp.Name)
		if sp.Value != "" {
			b.WriteString(": ")
			b.WriteString(sp.Value)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// --- references -------------------------------------------------------------

func (s *Store) attachRefs(items []Item, byID map[int64]int) error {
	rows, err := s.db.Query(`SELECT id, item_id, kind, title, url, filename, position
		FROM refs ORDER BY item_id, position, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r Reference
		if err := rows.Scan(&r.ID, &r.ItemID, &r.Kind, &r.Title, &r.URL, &r.Filename, &r.Position); err != nil {
			return err
		}
		if idx, ok := byID[r.ItemID]; ok {
			items[idx].Refs = append(items[idx].Refs, r)
		}
	}
	return rows.Err()
}

func (s *Store) AddReference(r Reference) (int64, error) {
	var next int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(position)+1, 0) FROM refs WHERE item_id = ?`, r.ItemID).Scan(&next)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO refs (item_id, kind, title, url, filename, position)
		VALUES (?,?,?,?,?,?)`, r.ItemID, r.Kind, r.Title, r.URL, r.Filename, next)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteReference removes a reference and reports the stored file, if any, so
// the caller can unlink it.
func (s *Store) DeleteReference(id int64) (itemID int64, filename string, err error) {
	err = s.db.QueryRow(`SELECT item_id, filename FROM refs WHERE id = ?`, id).Scan(&itemID, &filename)
	if err != nil {
		return 0, "", err
	}
	_, err = s.db.Exec(`DELETE FROM refs WHERE id = ?`, id)
	return itemID, filename, err
}

// --- price history ----------------------------------------------------------

// PricePoint is one observation of a price over time.
type PricePoint struct {
	Source   string
	Amount   float64
	Currency string
	SeenAt   time.Time
}

func (p PricePoint) Display() string { return formatMoney(p.Amount, p.Currency) }

// PriceHistory returns every observation for an item, oldest first.
func (s *Store) PriceHistory(itemID int64) ([]PricePoint, error) {
	rows, err := s.db.Query(`SELECT source, amount, currency, seen_at FROM price_history
		WHERE item_id = ? ORDER BY seen_at, id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PricePoint
	for rows.Next() {
		var p PricePoint
		var seen string
		if err := rows.Scan(&p.Source, &p.Amount, &p.Currency, &seen); err != nil {
			return nil, err
		}
		p.SeenAt, _ = time.Parse(time.RFC3339, seen)
		out = append(out, p)
	}
	return out, rows.Err()
}

// PriceChange describes how a source's price has moved since it was first seen.
type PriceChange struct {
	Source string
	First  float64
	Latest float64
	Points int
}

func (c PriceChange) Delta() float64 { return c.Latest - c.First }

func (c PriceChange) Direction() string {
	switch {
	case c.Latest > c.First:
		return "up"
	case c.Latest < c.First:
		return "down"
	}
	return "flat"
}

// PriceChanges summarises the history per source, skipping sources only ever
// seen once -- a single observation is not a trend.
func PriceChanges(points []PricePoint) []PriceChange {
	first := map[string]PricePoint{}
	last := map[string]PricePoint{}
	count := map[string]int{}
	var order []string

	for _, p := range points {
		if _, seen := first[p.Source]; !seen {
			first[p.Source] = p
			order = append(order, p.Source)
		}
		last[p.Source] = p
		count[p.Source]++
	}

	var out []PriceChange
	for _, src := range order {
		if count[src] < 2 || first[src].Amount == last[src].Amount {
			continue
		}
		out = append(out, PriceChange{
			Source: src, First: first[src].Amount,
			Latest: last[src].Amount, Points: count[src],
		})
	}
	return out
}

// --- projects ---------------------------------------------------------------

// Project is a build you are planning, with the parts it calls for.
type Project struct {
	ID        int64
	Name      string
	Notes     string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time

	Parts []ProjectPart
}

// ProjectPart is one line of a project's bill of materials. It either points at
// something on the shelf or names something not owned yet.
type ProjectPart struct {
	ID       int64
	ItemID   *int64
	Name     string
	Quantity int
	Note     string

	// Filled in when the line points at a real item.
	Item *Item
}

// Label is what to call this line: the item's name when it is owned, otherwise
// whatever was typed.
func (p ProjectPart) Label() string {
	if p.Item != nil {
		return p.Item.Name
	}
	return p.Name
}

// Have is how many are on the shelf. A line for a part you do not own has none
// by definition.
func (p ProjectPart) Have() int {
	if p.Item == nil {
		return 0
	}
	return p.Item.Quantity
}

func (p ProjectPart) Short() int {
	if n := p.Quantity - p.Have(); n > 0 {
		return n
	}
	return 0
}

func (p ProjectPart) Enough() bool { return p.Short() == 0 }

// Shortfall is every line the shelf cannot currently cover -- the answer to
// "what else do I need to buy".
func (p Project) Shortfall() []ProjectPart {
	var out []ProjectPart
	for _, part := range p.Parts {
		if !part.Enough() {
			out = append(out, part)
		}
	}
	return out
}

func (p Project) Ready() bool { return len(p.Shortfall()) == 0 && len(p.Parts) > 0 }

// EstimatedCost totals the cheapest known price of everything still missing.
func (p Project) EstimatedCost() float64 {
	var total float64
	for _, part := range p.Shortfall() {
		if part.Item == nil {
			continue
		}
		if best := part.Item.Best(); best != nil {
			total += best.Amount * float64(part.Short())
		}
	}
	return total
}

// Interfaces is the set of interfaces the project's owned parts use, which is
// what drives the "you may also need" suggestions.
func (p Project) Interfaces() []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range p.Parts {
		if part.Item == nil {
			continue
		}
		for _, i := range part.Item.Interfaces {
			if !seen[i] {
				seen[i] = true
				out = append(out, i)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *Store) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT id, name, notes, status, created_at, updated_at
		FROM projects ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Attach parts so the list can show progress without N queries.
	for i := range out {
		parts, err := s.projectParts(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Parts = parts
	}
	return out, nil
}

func scanProject(sc interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var created, updated string
	err := sc.Scan(&p.ID, &p.Name, &p.Notes, &p.Status, &created, &updated)
	if err != nil {
		return p, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, created)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return p, nil
}

func (s *Store) GetProject(id int64) (Project, error) {
	row := s.db.QueryRow(`SELECT id, name, notes, status, created_at, updated_at
		FROM projects WHERE id = ?`, id)
	p, err := scanProject(row)
	if err != nil {
		return p, err
	}
	p.Parts, err = s.projectParts(id)
	return p, err
}

// projectParts loads a bill of materials, resolving each line that points at
// something owned into the full item so stock and price are available.
func (s *Store) projectParts(projectID int64) ([]ProjectPart, error) {
	rows, err := s.db.Query(`SELECT id, item_id, name, quantity, note
		FROM project_parts WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var parts []ProjectPart
	var itemIDs []int64
	for rows.Next() {
		var p ProjectPart
		if err := rows.Scan(&p.ID, &p.ItemID, &p.Name, &p.Quantity, &p.Note); err != nil {
			return nil, err
		}
		if p.ItemID != nil {
			itemIDs = append(itemIDs, *p.ItemID)
		}
		parts = append(parts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(itemIDs) == 0 {
		return parts, nil
	}

	items, err := s.ListItems(Query{})
	if err != nil {
		return nil, err
	}
	byID := map[int64]*Item{}
	for i := range items {
		byID[items[i].ID] = &items[i]
	}
	for i := range parts {
		if parts[i].ItemID != nil {
			parts[i].Item = byID[*parts[i].ItemID]
		}
	}
	return parts, nil
}

func (s *Store) CreateProject(name, notes string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(`INSERT INTO projects (name, notes, status, created_at, updated_at)
		VALUES (?,?,?,?,?)`, name, notes, "planning", now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateProject(id int64, name, notes, status string) error {
	_, err := s.db.Exec(`UPDATE projects SET name=?, notes=?, status=?, updated_at=? WHERE id=?`,
		name, notes, status, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func (s *Store) DeleteProject(id int64) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

func (s *Store) AddProjectPart(projectID int64, itemID *int64, name string, qty int, note string) error {
	if qty < 1 {
		qty = 1
	}
	_, err := s.db.Exec(`INSERT INTO project_parts (project_id, item_id, name, quantity, note)
		VALUES (?,?,?,?,?)`, projectID, itemID, name, qty, note)
	if err != nil {
		return err
	}
	return s.touchProject(projectID)
}

func (s *Store) UpdateProjectPart(partID int64, qty int, note string) error {
	if qty < 1 {
		qty = 1
	}
	_, err := s.db.Exec(`UPDATE project_parts SET quantity = ?, note = ? WHERE id = ?`, qty, note, partID)
	return err
}

func (s *Store) DeleteProjectPart(partID int64) (int64, error) {
	var projectID int64
	if err := s.db.QueryRow(`SELECT project_id FROM project_parts WHERE id = ?`, partID).Scan(&projectID); err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`DELETE FROM project_parts WHERE id = ?`, partID); err != nil {
		return projectID, err
	}
	return projectID, s.touchProject(projectID)
}

func (s *Store) touchProject(id int64) error {
	_, err := s.db.Exec(`UPDATE projects SET updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// SuggestForProject proposes parts already on the shelf that speak one of the
// interfaces this project uses and are not in its bill of materials yet. It is
// a nudge from your own inventory -- "you have a level shifter that fits this"
// -- not a shopping recommendation.
func (s *Store) SuggestForProject(p Project, limit int) ([]Item, error) {
	want := p.Interfaces()
	if len(want) == 0 {
		return nil, nil
	}
	wanted := map[string]bool{}
	for _, i := range want {
		wanted[i] = true
	}
	inBOM := map[int64]bool{}
	for _, part := range p.Parts {
		if part.ItemID != nil {
			inBOM[*part.ItemID] = true
		}
	}

	items, err := s.ListItems(Query{})
	if err != nil {
		return nil, err
	}

	type scored struct {
		item    Item
		matches int
	}
	var hits []scored
	for _, it := range items {
		if inBOM[it.ID] || it.Quantity == 0 {
			continue
		}
		matches := 0
		for _, i := range it.Interfaces {
			if wanted[i] {
				matches++
			}
		}
		if matches > 0 {
			hits = append(hits, scored{it, matches})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].matches > hits[j].matches })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Item, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.item)
	}
	return out, nil
}
