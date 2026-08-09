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
| `SHOPIFY_SHOPS` | `thepihut.com,shop.pimoroni.com` | Shopify storefronts to search, comma separated. `none` disables them. |
| `NEXAR_CLIENT_ID` / `NEXAR_CLIENT_SECRET` | *(unset)* | Octopart credentials. The app mints and renews its own tokens. |
| `NEXAR_TOKEN` | *(unset)* | A pasted Nexar access token, for a quick try. These expire in 24 hours. |
| `THINGIVERSE_TOKEN` | *(unset)* | Free app token, to search Thingiverse for cases. Printables needs nothing. |
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
| **Any Shopify shop** | Yes. Shopify ships a public search endpoint (`/search/suggest.json`) that every store built on it exposes — no key, no account. The Pi Hut and Pimoroni are searched by default; `SHOPIFY_SHOPS` points it at the shops you actually buy from. |
| **Octopart** | Yes, with credentials. Nexar's GraphQL API answers with the manufacturer part number, distributor stock and pricing, factory lead times, specifications and a datasheet — the one source here that is about *parts* rather than products. Needs a Nexar plan with part quota. |
| **Amazon** | No. Automated requests get a bot interstitial with no product data in it, and their terms direct you to the Product Advertising API, which needs an affiliate account. |
| **AliExpress** | No. Server-side requests are bounced through redirects. |

Results from several shops are **interleaved** rather than concatenated, so one
shop with a big catalogue cannot crowd the others out of the visible list.

So rather than pretend, the search results include **Search there yourself**
links for Amazon, AliExpress and Octopart. Those open in your browser, where the
pages work normally — copy the URL back into the link importer, or just type the
price in.

#### Why not Amazon or AliExpress?

The short version: **they are the two that refuse, and there are plenty that
don't.** Adding Shopify covers a large slice of the maker world without an
account anywhere. The long version follows.

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

Image discovery falls through several conventions so it works on more than just
well-behaved shops: OpenGraph first, then `twitter:image`, `<meta itemprop>`,
`<link rel="image_src">`, schema.org JSON-LD, and finally the largest `<img>` on
the page — skipping logos, icons and spacers. Relative and protocol-relative URLs
are resolved against the page.

How well the rest works depends on the site: shops and wikis that emit OpenGraph
tags fill in almost everything, while a bare PDF datasheet link gives you little.
Sites that block non-browser traffic may refuse the request entirely.

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
- **Shopping list** exports just the shortfall as CSV.

### Reserving stock, and taking it

Set a project's status to **building** and its parts are reserved. Nothing moves
on the shelf — the count stays what it is — but every *other* project now sees
what's left rather than what's there:

> 3 pieces committed across 2 projects, you own 2

That's the case worth catching: two projects that each look satisfied on their
own page, and between them need more than you have. Contested lines say who else
is holding the part instead of just reading as "buy more".

**Mark built & take the parts** is the one action that changes stock without you
asking item by item, so it's a button rather than a side effect of a status
change. It deducts exactly what the list calls for, records what it actually
took, and **Put the parts back** returns precisely that — not what the parts list
says today, which may have changed since.

A line the shelf can't cover is deducted down to zero rather than driving stock
negative, and the shortfall is reported.

### Orders

An order is what closes the loop. A project says what it is short of, **Draft an
order for it** turns that into orders — *one per shop*, since that is how you
actually buy — and **Mark arrived** puts the parts on the shelf without counting
anything by hand.

Receiving is the exact inverse of building a project, and records enough to be
undone: **Un-receive** takes back precisely what it added. It also records what
you *actually paid* as that part's price, and how long the delivery *actually
took* as that shop's lead time.

That last part accumulates into something useful. The orders page shows how long
each shop really takes, measured from your own deliveries rather than from its
website:

> LCSC — 19 days on average over 4 orders (between 16 and 23) — 7 days slower
> than the 12 days quoted

Parts on their way show as **on order** on the item page, so something that is
out of stock but already bought doesn't look like something to buy again.

### Bills of materials

**Import a BOM** on a project reads a CSV from KiCad, EasyEDA, Altium or a
spreadsheet. Column names are matched by meaning, not position (`Qnty`, `Qty`,
`Quantity`; `Ref`, `Designator`, `RefDes`; `MPN`, `Part Number`, …), the preamble
KiCad writes above the header is skipped, and semicolon- and tab-separated files
work too. A bare `name,quantity` list pasted in a hurry also works.

Lines are matched against your inventory by part number first, then name, then
value — and `4700`, `4.7k` and `4k7` are recognised as the same resistor. **The
import shows you what it understood before it writes anything**, including how
each line matched, because a BOM silently pointed at the wrong part is worse than
one that is honestly blank.

Unmatched lines are kept by name and turn up in the shortfall list as things to
buy. **Export BOM** writes the list back out with what's on the shelf and what's
short, and reads back into this same importer.

### Pin budget

Give a project a **controller** — the board everything else hangs off — and it
works out whether that board actually has enough legs.

Demand comes from the interfaces already recorded against each part: I2C costs
two pins no matter how many sensors share it, SPI costs three plus a chip select
each, a UART costs a pair per device, PWM and ADC cost one each. Capacity is read
out of the controller's own specifications (a `GPIO: 24` line); when the board
doesn't say, the page says it doesn't know rather than inventing a number.

It also flags the two mistakes that cost an afternoon:

- **Duplicate I2C addresses** — two parts whose specs claim `0x76` can't both be
  on the bus. Parts listing several addresses are treated as jumper-selectable
  and aren't reported.
- **Logic level mismatches** — a 5 V part hung off a 3.3 V board, with a nudge
  towards a level shifter.

### Scanning labels

**Scan** points a phone camera at a drawer and lands on that part, with the
count buttons under your thumb — the half that makes the printed labels worth
printing. It reads this app's QR labels, its barcodes, and the manufacturer's
own barcode on the bag a part arrived in, matched against your part numbers.

The scanning uses the browser's own `BarcodeDetector`, so no library ships with
the page and nothing about the image leaves the device. That API exists in
Chrome (including on Android, which is the phone you'd be holding); Firefox and
Safari don't have it, so there the page says so and the field beside it takes a
typed code instead. A successful scan is shown *in place* rather than navigated
to, so the camera keeps running and a whole drawer can be counted in one go.

### Sub-assemblies

A project can be used as a line inside another project: a 5 V supply module you
design once and then put inside three different things.

Its own parts count towards the parent's shortfall, its reservations, and what
building the parent takes off the shelf — so a module used three times is
counted three times, right down through however many levels deep it goes. A
sub-assembly line says how many complete copies the shelf could supply
(*"3 buildable from stock"*), and a project that would end up containing itself
is refused rather than looping.

### Pin map

The budget counts pins. The map records *which* pin went where, which is the only
way to catch the mistake counting cannot see: the same pin used twice.

`GPIO21`, `gpio 21`, `IO21` and `io_21` are recognised as one pin, so a clash is
caught however it was spelled. Power rails and bus lines are exempt, because
several devices on `SDA` is what a bus is — but the same pin carrying SDA for one
part and CS for another is flagged on both rows. **Start from what the parts
need** proposes every connection the parts imply (an I2C sensor needs SDA and
SCL, and there is no guessing involved); fill in the pin numbers you used and
they all go in at once. **Wiring card** exports the lot as CSV for the bench.

### Build log

A dated notebook per project: what you did, what it measured, which pin you got
wrong the first time. Entries take photos, and are attributed to whoever wrote
them once accounts exist.

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

**Find documentation** does the hunt for you. It follows the item's product link,
searches the shops it can reach by part number, asks Octopart if it is
configured — and then follows the trail one hop further, because that is where
the documents actually are. A product page links a *learn guide*; the guide has a
*pinouts* page; the diagrams are on that. Following that chain is the difference
between finding nothing and finding this, for a HUZZAH32:

> Found 5 pinouts and 3 documents and a photo — from adafruit.com, Pimoroni,
> learn.adafruit.com

Pinout and schematic **images are downloaded**, so they display on the item page
and survive the source going away; datasheets stay links, since a datasheet is
better fetched fresh than kept stale. A part with no photo gets one from the same
page. Running it twice adds nothing twice.

**References** are datasheets, pinouts, manuals and schematics. A reference can be
a link or a stored image. A pinout given as a URL is *downloaded*, so it shows on
the page and survives the source moving it; if the image can't be fetched the link
is still kept, and the page says why it stayed a link.

### Footprints

Give a part a KiCad footprint name and its land pattern is **drawn to scale** on
the item page: pads, drill holes, silkscreen outline, the courtyard it claims on
the board, and a ring round pin 1 so you can see which way round it goes. Under
it: the real size in millimetres, the pad pitch (and whether that is breadboard
0.1"), the pad count and whether it is through-hole or surface mount.

`Resistor_SMD:R_0805_2012Metric` is taken at its word; a bare `R_0805_2012Metric`
has its library guessed. A **BOM import fills this in on its own**, since a
schematic's footprint column is exactly this. For anything not in the official
library, upload the `.kicad_mod`.

The file is fetched once from KiCad's own library — the project's GitLab, falling
back to its GitHub mirror — and then cached, so the drawing works offline and an
item page never waits on the network. Nothing is added to the binary to do this:
a `.kicad_mod` is an s-expression, so it is parsed and rendered to SVG in about
500 lines rather than by pulling in a dependency.

### Cases and 3D models

**Find a case** on any part searches the model sites for something somebody has
already drawn for it — a case, a bracket, a DIN-rail mount — and shows the
renders, the author, and the download count, which is the only real review
signal those sites have. A model printed ten thousand times works.

[Printables](https://www.printables.com) is searched through the same API its own
website uses: no key, no account. [Thingiverse](https://www.thingiverse.com) joins
in if you set `THINGIVERSE_TOKEN` (free, but it does mean registering).
MakerWorld, Thangs, Cults3D and GrabCAD serve a bot challenge or a signed-in-only
page to anything that is not a browser, so rather than pretend, the page offers
their own searches as links — paste the result back to keep it.

Thumbnails are fetched through the app rather than by your browser, so opening a
page of results does not tell the model site who you are, and the pictures still
appear on an install with no direct internet access. That proxy only serves the
model sites it searches; it is not a general-purpose one pointed at your network.

### Measuring a model before you print it

A site's thumbnail is somebody's render. The mesh is the thing that has to fit.

Give a kept model its **STL** — upload it, or paste a direct link — and the
geometry is read, measured and drawn:

> Raspberry Pi 5 case is 60.0 × 31.0 × 48.0 mm (225.7k triangles)

with a shaded three-quarter view generated from the actual triangles, and a
warning when it will not fit a 220 × 220 × 250 mm bed in one piece. Both spellings
of STL are read — binary and ASCII — and the file's *length* decides which,
because plenty of exporters write the word "solid" into a binary file's header.

The STL itself is **not kept**: only its measurements and the drawing. A hundred
models at forty megabytes each is not something an inventory should quietly start
storing on your behalf, and the file is still on the model site, which is where
the link goes.

As with the footprints, nothing was added to the binary to do this. An STL is a
triangle soup, so it is parsed and rasterised with a z-buffer straight to a PNG —
a real print is a couple of hundred thousand triangles, and an SVG with that many
paths in it is not a preview, it is a denial of service on the browser.

### Octopart

Octopart is different from the shops: it indexes *parts*, so a lookup by
manufacturer part number returns specifications, a datasheet, and what several
distributors charge with their stock levels and factory lead times. All of that
maps directly onto what this app already stores.

**Fill in from Octopart** on an item page looks up its part number and writes
back the specifications, attaches the datasheet, and records up to five
distributor prices with their lead times. It needs a part number rather than a
product name — "ESP32-WROOM-32E", not "ESP32 board" — because a name matches the
wrong silicon far too easily, and it refuses rather than guessing.

**Credentials.** Nexar uses OAuth. Set `NEXAR_CLIENT_ID` and
`NEXAR_CLIENT_SECRET` and the app mints and renews its own tokens. `NEXAR_TOKEN`
accepts a pasted access token instead, which is fine for a quick try but expires
within 24 hours — when it does, the app says so and points at the client
credentials rather than failing with a bare 401.

**Plan quota.** Nexar's free tier includes no part quota. Every supply query then
answers HTTP 200 with `You have exceeded your part limit of 0` inside the GraphQL
errors array. That message is passed through verbatim to the search notes and the
item page, because "no results" would send you hunting for a bug that isn't
there.

### Quick add

The dashboard's **Quick add** box takes a product link and does the rest in one
step: fetches the page, creates the item, downloads the photo, records the price
with its shipping estimate, and attaches every datasheet, schematic and pinout
the page links to. A SparkFun product page typically arrives with five
references already attached.

The full add form is still there and still better when a page guesses wrong —
quick add is for "I just bought this, put it in the inventory".

### Search as you type

Every search box shows matches from your own inventory as you type, with a
thumbnail, where it lives, the price and how many you have. It answers the
question you usually have mid-typing — *do I already own one of these?* — without
a page load. Arrow keys walk the list, Enter opens the highlighted row.

Requests are cancelled as you keep typing, so a slow answer for "es" can never
overwrite the results for "esp32".

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

**Labels** is in the header on every page (or `/labels`, or the button on any
grid). It prints a sheet for whatever the
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
- **Footprint** — a land pattern drawn to scale, with the part's real size in mm.
- **Cases** — printable models found, kept, measured and drawn from their own geometry.
- **Reorder at** — each item has its own low-stock line, set on its edit page.
  Two Raspberry Pis is plenty; two 0805 resistors is nothing. The dashboard's
  *Running low* list measures **free** stock, so parts reserved by a build in
  progress count against you.

### Calculators

**Calculators** in the header holds the sums you'd otherwise do on your phone:
LED series resistor, resistor divider, RC filter, PCB trace width (IPC-2221),
Ohm's law, linear regulator heat, battery life.

Every answer that lands on a component value is then checked against your own
shelf — "you already own a 330R, 10% off, in Drawer 3" — which is the part a
website calculator can't do. Values are rounded to the nearest E24 part you can
actually buy, and inputs accept engineering notation (`4k7`, `100n`, `220R`)
wherever a plain number works.

Each calculator is a plain GET form: the answer is in the URL, so it can be
bookmarked or shared, and none of it needs JavaScript.

Everything works without JavaScript except the link import, which needs it. JS
otherwise only upgrades the count buttons to update in place, and adds
drag-and-drop, the tag picker and the lightbox.

## Access from outside the LAN

The app has no TLS of its own — put it behind whatever you already run (Caddy,
Traefik, nginx, a Tailscale/WireGuard tunnel). Set `AUTH_PASSWORD` if it will be
reachable from anywhere untrusted. The session cookie is HMAC-signed with a key
generated on first run and stored at `$DATA_DIR/session.key`; deleting that file
signs everyone out.

### Accounts

Named accounts are optional and sit on top of that rather than replacing it. With
no accounts, nothing changes: the shared password (or no password) is the whole
door, and the activity log can only say *someone*.

Add one under **Health & backup** and sign-in starts asking for a username,
sessions carry who you are, and every change is attributed. The first account is
always an administrator — an install with only members could never manage itself
— and the last administrator can't be deleted or demoted. Anyone can change their
own password; only an administrator can add accounts, hand out administrator
rights, or restore a backup.

Passwords are stored as PBKDF2-HMAC-SHA256 with 210,000 iterations and a random
salt per password, in a self-describing format so the iteration count can be
raised later without invalidating anyone's password.

## Health and backups

**Health & backup** (the ⚙ in the header) is the operator's page: schema version,
a SQLite integrity check, database and photo sizes, free disk, row counts, and
what the install is actually configured to do — how the door works, whether URL
imports may reach the LAN, which shops are searched, whether Octopart has
credentials.

It also reports **photo drift** both ways: files on disk nothing points at
(harmless, sweepable) and rows pointing at files that are gone (worth knowing
about). Missing thumbnails can be rebuilt from the originals.

**Download backup** gives you one zip holding a consistent snapshot of the
database — taken with `VACUUM INTO`, not a copy of a file being written to — and
every photo.

**Restore** replaces everything currently in the install; it is not a merge, and
it wants you to type `replace` to confirm. It works by attaching the backup as a
second database and copying it table by table inside a single transaction, so the
app doesn't need restarting, a failure halfway through rolls back to what was
there before, and a backup taken several versions ago is migrated forward on the
way in. Thumbnails are regenerated afterwards, since a backup only carries the
originals.

You can still just copy `data/` if you'd rather. It holds `inventory.db` (plus its
WAL sidecar files), `photos/`, and `session.key`. Nothing else is stored anywhere.
The session key deliberately lives outside the database, which is why restoring a
backup doesn't sign you out.

## Activity log

Every change to an item, a project or an account is recorded with who, what, when
and enough detail to recognise it — including changes that go through the +/−
buttons. **Activity** filters by kind and links back to whatever was touched.
Entries older than 90 days can be pruned from the admin page.

## Development

```sh
go test ./...     # store, search, tags, folders, prices, photos, EXIF, SSRF, HTTP,
                  # reservations, BOM parsing, calculators, pin budget, backup/restore,
                  # accounts, orders, scanning, sub-assemblies, pin maps, KiCad
                  # footprint parsing, STL parsing and rendering, model search,
                  # and the upgrade path from an older schema
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
| `handlers_quick.go` | One-paste quick add and the type-ahead endpoint |
| `search_shopify.go` | Shopify storefront search |
| `search_nexar.go` | Octopart via Nexar: OAuth, GraphQL, part detail |
| `labels.go` | QR / barcode generation and the print sheet |
| `fetch.go` | Outbound fetching, SSRF guards, OpenGraph parsing |
| `images.go` | Upload validation, thumbnails, EXIF orientation |
| `auth.go` | Optional shared-password sessions |
| `templates/`, `static/` | UI — embedded into the binary at build time |

Schema changes go in `migrations` in `migrate.go` as a new entry; the database
records how far it has got in `PRAGMA user_version` and applies whatever is
missing on the next start.
