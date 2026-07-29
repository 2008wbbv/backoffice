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

### Prices

Each item can carry one price per shop — Adafruit $19.95, AliExpress $4.20,
whatever you paid at a market stall. Search and link imports record theirs
automatically; the rest you add on the item page.

The cheapest price shows as a badge on the item's card, and the dashboard totals
the shelf: every item's cheapest price multiplied by how many you have.

Note that **Value / rating** is a different field — it is the electrical value
("10kΩ 1% 0805"), not money.

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
| `migrate.go` | Schema migrations, applied on startup |
| `handlers.go` | Item and grid handlers |
| `handlers_folders.go` | Dashboard and folder handlers |
| `handlers_import.go` | Import-from-URL endpoints |
| `handlers_search.go` | Part search, prices and tag icons |
| `fetch.go` | Outbound fetching, SSRF guards, OpenGraph parsing |
| `images.go` | Upload validation, thumbnails, EXIF orientation |
| `auth.go` | Optional shared-password sessions |
| `templates/`, `static/` | UI — embedded into the binary at build time |

Schema changes go in `migrations` in `migrate.go` as a new entry; the database
records how far it has got in `PRAGMA user_version` and applies whatever is
missing on the next start.
