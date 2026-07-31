# Backoffice

A small self-hosted inventory for homelab parts — boards, SBCs, ESP32s, M5 sticks,
modules, sensors, resistors, whatever is in the drawers. Photos, a count, and where
it lives, organised into folders however you actually think about your stuff.

One static binary, one SQLite file, one folder of photos. No database server, no
Node build, no config file.

## Run it

**Docker Compose** (recommended):

```sh
docker compose up -d
```

Open <http://localhost:8080>. That's the whole setup — `./data` is created on first
run and holds everything.

**Straight binary**, if you'd rather not use Docker:

```sh
go build -o backoffice .
./backoffice                   # http://localhost:8080, data in ./data
```

Cross-compiling for a Pi or other ARM box, from any machine with Go:

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o backoffice .        # Pi 4/5, 64-bit
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -o backoffice .  # Pi Zero 2 / 32-bit
```

Copy the binary over and run it — it has no runtime dependencies at all, not even
libc. Templates, CSS and JS are compiled into the executable.

Upgrading is just replacing the binary. The database migrates itself on startup;
there is no migration command to run and no downtime step.

## Configuration

Everything is optional. The defaults are the intended setup.

| Variable | Default | What it does |
| --- | --- | --- |
| `PORT` | `8080` | Port to listen on. Accepts `8080`, `:8080` or `127.0.0.1:8080`. |
| `DATA_DIR` | `./data` | Where the database and photos live. |
| `AUTH_PASSWORD` | *(unset)* | If set, the whole app requires this password. If unset, no login at all. |
| `SITE_TITLE` | `Backoffice` | Name in the header and browser tab. |
| `BASE_URL` | *(request host)* | Public address printed into QR labels. Set this when behind a reverse proxy. |
| `ALLOW_PRIVATE_FETCH` | *(unset)* | Let "import from a link" reach LAN addresses. See below. |
| `TZ` | `UTC` | Affects the "updated 3 hours ago" timestamps. |

## Using it

### Folders

The dashboard is organised into folders — "3D printer", "Bench drawers",
"Sensors" — and they nest as deep as you like. A folder shows its own items, with
a toggle to include everything in its subfolders, and each folder card counts its
whole subtree.

Folders are deliberately *not* the same thing as **location**. A location is where
a part physically sits ("Shelf B / drawer 3"); a folder is how you think about it
("the 3D printer pile"). Items can have both, either, or neither — anything with no
folder shows up under **Unfiled**.

Deleting a folder never deletes items. Its subfolders go with it, and everything
inside becomes unfiled.

### Tags

Tags are first-class: type them comma-separated, or click any existing tag to
attach it. They're matched case-insensitively, so `SMD` and `smd` are one tag.

Clicking tags on the dashboard or in the filter row **stacks** them — picking
`smd` and then `0805` shows only items carrying both. Tags that stop being used
disappear from the list on their own.

### Finding parts without typing them out

The add/edit form has two shortcuts.

**Search for a part** takes a name — "esp32 feather", "ds18b20", "10k resistor" —
and looks it up for you, no link needed. Results come back with a photo, the
price and stock, and clicking one fills the form in.

Which shops can actually be searched from a server is not a matter of taste:

| Source | Searchable from the server? |
| --- | --- |
| **Adafruit** | Yes. It publishes its whole catalogue as JSON, which is cached for six hours and searched locally, so only the first search waits. |
| **Amazon** | No. Automated requests get a bot interstitial with no product data in it, and their terms direct you to the Product Advertising API, which needs an affiliate account. |
| **AliExpress** | No. Server-side requests are bounced through redirects. |

So rather than pretend, the search results include **Search there yourself**
links for Amazon, AliExpress and Octopart. Those open in your browser, where the
pages work normally — copy the URL back into the link importer, or just type the
price in.

#### Why not Amazon or AliExpress?

Not for lack of trying — here is what actually happens when a server asks.

**Amazon** returns HTTP 200 to a search request and to product pages, which looks
promising until you read the body: a search for "esp32" came back as 285 KB
containing **zero** product results, and a product page came back as 410 KB with
no `og:` tags, no `productTitle`, and no price markup at all. It is an
interstitial dressed as a page. That is deliberate — their terms of service
direct automated access to the Product Advertising API, which requires an
Associates (affiliate) account with qualifying sales. If you have those
credentials, a provider for it is roughly a day's work and slots into the same
`SearchProvider` interface as Adafruit; the blocker is the account, not the code.

**AliExpress** bounces server-side requests through redirects — `aliexpress.com`
→ `aliexpress.us` → back again — and never serves the product page. They also
have an affiliate/open-platform API behind an approved account.

**A general web search** as a fallback does not help either: DuckDuckGo's HTML
endpoint answers with an anti-bot challenge (HTTP 202, no results). Bing and
Brave both work well but need an API key.

The honest summary: **every route to Amazon and AliExpress runs through an
account you have to apply for.** Scraping them would produce something that
silently returns nothing and rots the first time they change a template, which
is worse than not having it. What is here instead — deep links out, plus the link
importer and manual price entry for when you come back with a URL — costs you two
clicks and never lies about what it knows.

**Import from a link** takes a product page, datasheet or wiki URL and reads its
OpenGraph metadata: name, description, price, part number and a photo. Nothing is
saved until you press Add/Save, and every field stays editable — both shortcuts
are a starting point, not an authority.

You can also paste an image URL directly, on the form or on an existing item's
page ("…or pull one from the web"). Give it a product page instead of an image and
it will find that page's preview image.

How well this works depends on the site: shops and wikis that emit OpenGraph tags
fill in almost everything, while a bare PDF datasheet link gives you little. Sites
that block non-browser traffic may refuse the request entirely.

**A note on what this does:** it makes your server fetch a URL you paste. Because
the server usually sits inside your home network, it could otherwise be used to
reach things you didn't intend — a router admin page, a NAS, a cloud metadata
endpoint. So private, loopback, link-local and carrier-NAT addresses are refused
by default, on every redirect hop, checked against the address actually dialled
rather than the hostname. If you want to import from something on your own network
(a LAN wiki, a local parts server), set `ALLOW_PRIVATE_FETCH=1` — but only do that
if you trust everyone who can reach the app.

### Projects

A project is a parts list for something you're building. Add lines for parts you
own and parts you don't, say how many each needs, and the project page answers
the question you actually have: **what am I still missing?**

- **What you still need** lists every line the shelf cannot cover, with how many
  short you are, and an estimated cost from the cheapest known price.
- **You already own these** suggests parts from your own inventory that speak the
  same interfaces as what's already on the list — a nudge toward the level
  shifter you forgot you had, not a shopping recommendation.
- Stock changes are picked up live: restock a part and its line clears itself.

### IO and interfaces

Each item records what it speaks and needs — I2C, SPI, 1-Wire, 3V3 logic, USB-C,
WiFi, and so on — from a fixed vocabulary rather than free text, so a project can
actually reason about it. Anything outside the list is discarded rather than
stored, which keeps the filter row free of typos.

Interfaces show as icons on cards, filter like tags (picking two narrows), and
drive the project suggestions.

### Specifications, datasheets and pinouts

**Specifications** are free-form `name: value` lines — "Logic level: 3.3V",
"Flash: 8MB" — typed one per line and shown as a table.

**References** are datasheets, pinouts, manuals and schematics. A reference can be
a link or a stored image. A pinout given as a URL is *downloaded*, so it shows on
the page and survives the source moving it; if the image can't be fetched the link
is still kept, and the page says why it stayed a link.

### Prices

Each item can carry one price per shop — Adafruit $19.95, AliExpress $4.20,
whatever you paid at a market stall. Search and link imports record theirs
automatically; the rest you add on the item page.

The cheapest price shows as a badge on the item's card, and the dashboard totals
the shelf: every item's cheapest price multiplied by how many you have.

**Shipping times** are recorded per shop alongside the price, because the cheapest
source is rarely the fastest one. Well-known shops get a sensible default when you
leave the field blank (AliExpress 30 days, Amazon 2, Adafruit 5) and anything you
type wins. The item page calls it out when they differ: *"Cheapest is AliExpress;
Amazon arrives soonest (~2 days)."*

**Price tracking** records every change. Re-saving the same figure is not recorded
— only movements — so the item page can show "Adafruit: $19.95 → $22.00 over 3
checks". **Check price now** re-looks-up a part in the sources that can be
searched, and refuses to overwrite anything when the closest match is a different
part number.

Note that **Value / rating** is a different field — it is the electrical value
("10kΩ 1% 0805"), not money.

### Labels: QR codes and barcodes

**Labels** (the button on the grid, or `/labels`) prints a sheet for whatever the
grid is currently showing — a folder, a location, a search. Two kinds:

- **QR codes** carry the item's full URL. Point a phone camera at a drawer and its
  page opens; no app, no scanner, nothing to install. This is the one that makes
  the inventory usable *at the bench* rather than at a desk.
- **Code 128 barcodes** carry the part number, falling back to `BO-<id>` when there
  isn't one, or when the part number has non-ASCII characters or is too long to
  print legibly. A USB barcode scanner behaves like a keyboard, so these type
  straight into the search box.

The QR codes point at whatever `BASE_URL` says, falling back to the address the
request arrived on. Behind a reverse proxy the server cannot see its own public
name, so set `BASE_URL` — the labels page shows you which address it is baking in
before you print anything.

Print styling is built in: the page chrome disappears, the sheet becomes a
three-column grid on white, and labels never break across pages.

### Everything else

- **Add an item** — name is the only required field.
- **Tags carry icons.** An icon is guessed from the tag name the first time a tag
  is used (`wifi` → 📶, `temperature` → 🌡, `battery` → 🔋); anything unrecognised
  still gets a stable one. Change any of them under **Tags** on the dashboard.
- **Locations and types configure themselves.** Type "Drawer 3" once and it becomes
  a filter chip and an autocomplete suggestion.
- **Counts** — the `−` / `+` buttons adjust stock without opening the item. They
  never go below zero.
- **Search** matches every word against name, type, location, part number, value,
  tags and notes. `esp32 drawer` finds ESP32s stored in a drawer.
- **Photos** — drop them on an item, or use the file picker, which opens the camera
  on a phone. Thumbnails are generated automatically and rotated to match the
  photo's EXIF orientation, so portrait phone shots aren't sideways. Hover a
  thumbnail for ★ (make cover) and × (delete).
- **Drag and drop** — drag a product link from another tab onto the add form and
  it imports; drag an image file and it goes into the photo picker.
- **Keyboard** — `/` focuses search, `n` opens the add form.
- **CSV** — the `CSV` button exports the full inventory, folders and tags included.

Everything works without JavaScript except the link import, which needs it. JS
otherwise only upgrades the count buttons to update in place, and adds
drag-and-drop, the tag picker and the lightbox.

## Access from outside the LAN

The app has no TLS of its own — put it behind whatever you already run (Caddy,
Traefik, nginx, a Tailscale/WireGuard tunnel). Set `AUTH_PASSWORD` if it will be
reachable from anywhere untrusted. The session cookie is HMAC-signed with a key
generated on first run and stored at `$DATA_DIR/session.key`; deleting that file
signs everyone out.

## Backups

Copy `data/`. It holds `inventory.db` (plus its WAL sidecar files), `photos/`, and
`session.key`. Nothing else is stored anywhere.

To snapshot the database safely while the app is running:

```sh
sqlite3 data/inventory.db ".backup 'backup.db'"
```

## Development

```sh
go test ./...     # store, search, tags, folders, prices, photos, EXIF, SSRF, HTTP
go vet ./...
```

Layout:

| File | Contents |
| --- | --- |
| `main.go` | Config, routes, template helpers |
| `db.go` | Item/folder/tag/price queries and the filter model |
| `search.go` | Part search providers and the Adafruit catalogue |
| `tags.go` | Tag icon defaults |
| `parts.go` | Interfaces, specs, references, price history, projects |
| `migrate.go` | Schema migrations, applied on startup |
| `handlers.go` | Item and grid handlers |
| `handlers_folders.go` | Dashboard and folder handlers |
| `handlers_import.go` | Import-from-URL endpoints |
| `handlers_search.go` | Part search, prices and tag icons |
| `handlers_projects.go` | Projects, references and price refresh |
| `fetch.go` | Outbound fetching, SSRF guards, OpenGraph parsing |
| `images.go` | Upload validation, thumbnails, EXIF orientation |
| `auth.go` | Optional shared-password sessions |
| `templates/`, `static/` | UI — embedded into the binary at build time |

Schema changes go in `migrations` in `migrate.go` as a new entry; the database
records how far it has got in `PRAGMA user_version` and applies whatever is
missing on the next start.
