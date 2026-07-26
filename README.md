# Parts Bin

A small self-hosted inventory for homelab parts — boards, SBCs, ESP32s, M5 sticks,
modules, sensors, resistors, whatever is in the drawers. Photos, a count, and where
it lives.

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
go build -o partsbin .
./partsbin                     # http://localhost:8080, data in ./data
```

Cross-compiling for a Pi or other ARM box, from any machine with Go:

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o partsbin .   # Pi 4/5, 64-bit
GOOS=linux GOARCH=arm   GOARM=7 CGO_ENABLED=0 go build -o partsbin .   # Pi Zero 2 / 32-bit
```

Copy the binary over and run it — it has no runtime dependencies at all, not even
libc. Templates, CSS and JS are compiled into the executable.

## Configuration

Everything is optional. The defaults are the intended setup.

| Variable | Default | What it does |
| --- | --- | --- |
| `PORT` | `8080` | Port to listen on. Accepts `8080` or `:8080` or `127.0.0.1:8080`. |
| `DATA_DIR` | `./data` | Where the database and photos live. |
| `AUTH_PASSWORD` | *(unset)* | If set, the whole app requires this password. If unset, no login at all. |
| `SITE_TITLE` | `Parts Bin` | Name in the header and browser tab. |
| `TZ` | `UTC` | Affects the "updated 3 hours ago" timestamps. |

## Using it

- **Add an item** — name is the only required field. Everything else (quantity,
  location, type, value, part number, tags, link, notes, photos) is optional and
  can be filled in later.
- **Locations and types configure themselves.** Type "Drawer 3" once and it becomes
  a filter chip and an autocomplete suggestion. There is no separate screen for
  setting up a location tree, because you don't need one.
- **Counts** — the `−` / `+` buttons on the grid adjust stock without opening the
  item. They never go below zero.
- **Search** matches every word against name, type, location, part number, value,
  tags and notes. `esp32 drawer` finds ESP32s stored in a drawer.
- **Photos** — drop them on an item, or use the file picker, which opens the camera
  on a phone. Thumbnails are generated automatically and rotated to match the photo's
  EXIF orientation, so portrait phone shots aren't sideways. Click a thumbnail's ★ to
  make it the cover, × to delete it.
- **Keyboard** — `/` focuses search, `n` opens the add form.
- **CSV** — the `CSV` button exports the full inventory.

Everything works without JavaScript; JS only upgrades the count buttons to update
in place, adds drag-and-drop and the lightbox.

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
go test ./...     # store, search, photo pipeline, EXIF, auth, HTTP round-trips
go vet ./...
```

Layout:

| File | Contents |
| --- | --- |
| `main.go` | Config, routes, template helpers |
| `db.go` | Schema and every SQL query |
| `handlers.go` | HTTP handlers |
| `images.go` | Upload validation, thumbnails, EXIF orientation |
| `auth.go` | Optional shared-password sessions |
| `templates/`, `static/` | UI — embedded into the binary at build time |

The schema is created on startup if missing, so there's no migration step to run.
