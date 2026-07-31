package main

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type Config struct {
	Addr         string
	DataDir      string
	Password     string
	Title        string
	BaseURL      string   // public address, for the URLs printed into QR codes
	ShopifyShops []string // storefronts to search, by hostname
	AllowPrivate bool     // let URL imports reach LAN addresses
}

func configFromEnv() Config {
	c := Config{
		Addr:     env("PORT", "8080"),
		DataDir:  env("DATA_DIR", "./data"),
		Password: os.Getenv("AUTH_PASSWORD"),
		Title:    env("SITE_TITLE", "Backoffice"),
		BaseURL:  os.Getenv("BASE_URL"),
	}
	c.ShopifyShops = defaultShopifyShops
	if raw := strings.TrimSpace(os.Getenv("SHOPIFY_SHOPS")); raw != "" {
		c.ShopifyShops = nil
		for _, shop := range strings.Split(raw, ",") {
			if shop = strings.TrimSpace(shop); shop != "" && shop != "none" {
				c.ShopifyShops = append(c.ShopifyShops, shop)
			}
		}
	}
	switch strings.ToLower(os.Getenv("ALLOW_PRIVATE_FETCH")) {
	case "1", "true", "yes":
		c.AllowPrivate = true
	}
	if !strings.Contains(c.Addr, ":") {
		c.Addr = ":" + c.Addr
	}
	return c
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type App struct {
	cfg     Config
	store   *Store
	photos  *PhotoStore
	tmpl    *template.Template
	auth    *Auth
	fetcher *Fetcher
	search  *SearchHub
}

func main() {
	log.SetFlags(log.Ltime)
	cfg := configFromEnv()

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("cannot create data dir %s: %v", cfg.DataDir, err)
	}
	db, err := openDB(filepath.Join(cfg.DataDir, "inventory.db"))
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()

	photos, err := NewPhotoStore(filepath.Join(cfg.DataDir, "photos"))
	if err != nil {
		log.Fatalf("photo store: %v", err)
	}
	auth, err := NewAuth(cfg.Password, filepath.Join(cfg.DataDir, "session.key"))
	if err != nil {
		log.Fatalf("auth: %v", err)
	}

	fetcher := NewFetcher(cfg.AllowPrivate)
	app := &App{
		cfg:     cfg,
		store:   &Store{db: db},
		photos:  photos,
		tmpl:    mustTemplates(),
		auth:    auth,
		fetcher: fetcher,
		search:  NewSearchHub(fetcher, cfg.ShopifyShops),
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      2 * time.Minute, // uploads
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		mode := "open (no password set)"
		if auth.Enabled() {
			mode = "password protected"
		}
		fetch := "public hosts only"
		if cfg.AllowPrivate {
			fetch = "private addresses allowed"
		}
		log.Printf("%s listening on http://localhost%s  data=%s  auth=%s  url-import=%s",
			cfg.Title, cfg.Addr, cfg.DataDir, mode, fetch)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Print("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()

	// Public: health check and assets.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /static/", http.FileServer(http.FS(staticFS)))
	mux.HandleFunc("GET /login", a.handleLoginForm)
	mux.HandleFunc("POST /login", a.handleLogin)
	mux.HandleFunc("POST /logout", a.handleLogout)

	// Everything below requires a session when AUTH_PASSWORD is set.
	protected := http.NewServeMux()
	protected.HandleFunc("GET /{$}", a.handleDashboard)
	protected.HandleFunc("GET /items", a.handleIndex)
	protected.HandleFunc("GET /items/new", a.handleNewForm)
	protected.HandleFunc("GET /items/{id}", a.handleItem)
	protected.HandleFunc("GET /items/{id}/edit", a.handleEditForm)
	protected.HandleFunc("POST /items", a.handleCreate)
	protected.HandleFunc("POST /items/{id}", a.handleUpdate)
	protected.HandleFunc("POST /items/{id}/delete", a.handleDelete)
	protected.HandleFunc("POST /items/{id}/quantity", a.handleQuantity)
	protected.HandleFunc("POST /items/{id}/photos", a.handleUploadPhotos)
	protected.HandleFunc("POST /photos/{id}/delete", a.handleDeletePhoto)
	protected.HandleFunc("POST /photos/{id}/cover", a.handleCoverPhoto)
	protected.HandleFunc("GET /media/thumb/{name}", a.handleThumb)
	protected.HandleFunc("GET /media/{name}", a.handleMedia)
	protected.HandleFunc("POST /items/{id}/photos/url", a.handlePhotoFromURL)
	protected.HandleFunc("POST /items/{id}/move", a.handleMoveItem)
	protected.HandleFunc("GET /export.csv", a.handleExportCSV)

	// Folders
	protected.HandleFunc("GET /folders/{id}", a.handleFolder)
	protected.HandleFunc("POST /folders", a.handleCreateFolder)
	protected.HandleFunc("POST /folders/{id}", a.handleRenameFolder)
	protected.HandleFunc("POST /folders/{id}/delete", a.handleDeleteFolder)

	// Prices
	protected.HandleFunc("POST /items/{id}/price", a.handleSetPrice)
	protected.HandleFunc("POST /items/{id}/price/delete", a.handleDeletePrice)

	// Tags
	protected.HandleFunc("GET /tags", a.handleTags)
	protected.HandleFunc("POST /tags/icon", a.handleSetTagIcon)

	// Projects: a bill of materials plus what the shelf cannot cover.
	protected.HandleFunc("GET /projects", a.handleProjects)
	protected.HandleFunc("POST /projects", a.handleCreateProject)
	protected.HandleFunc("GET /projects/{id}", a.handleProject)
	protected.HandleFunc("POST /projects/{id}", a.handleUpdateProject)
	protected.HandleFunc("POST /projects/{id}/delete", a.handleDeleteProject)
	protected.HandleFunc("POST /projects/{id}/parts", a.handleAddProjectPart)
	protected.HandleFunc("POST /project-parts/{id}/delete", a.handleDeleteProjectPart)

	// Datasheets, pinouts and manuals.
	protected.HandleFunc("POST /items/{id}/refs", a.handleAddReference)
	protected.HandleFunc("POST /refs/{id}/delete", a.handleDeleteReference)
	protected.HandleFunc("POST /items/{id}/price/refresh", a.handleRefreshPrice)

	// Labels: a QR code opens the item on a phone, a barcode feeds a scanner.
	protected.HandleFunc("GET /labels", a.handleLabels)
	protected.HandleFunc("GET /items/{id}/qr.png", a.handleItemQR)
	protected.HandleFunc("GET /items/{id}/barcode.png", a.handleItemBarcode)

	// One-step add from a pasted link, and type-ahead over the inventory.
	protected.HandleFunc("POST /items/quick", a.handleQuickAdd)
	protected.HandleFunc("GET /items/search.json", a.handleLiveSearch)

	// Look a part up by name, or read a URL the person pasted.
	protected.HandleFunc("POST /import/search", a.handleSearch)
	protected.HandleFunc("POST /import/preview", a.handleImportPreview)

	mux.Handle("/", a.auth.Require(protected))
	return logRequests(mux)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/media/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

func mustTemplates() *template.Template {
	return template.Must(template.New("").Funcs(templateFuncs()).ParseFS(templateFS, "templates/*.html"))
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"since": func(t time.Time) string {
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				return plural(int(d.Minutes()), "minute") + " ago"
			case d < 24*time.Hour:
				return plural(int(d.Hours()), "hour") + " ago"
			case d < 30*24*time.Hour:
				return plural(int(d.Hours()/24), "day") + " ago"
			default:
				return t.Local().Format("2 Jan 2006")
			}
		},
		"add":   func(a, b int) int { return a + b },
		"money": func(amount float64) string { return formatMoney(amount, "USD") },

		// dict lets a page pass several named values into a shared partial.
		"dict": func(pairs ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(pairs); i += 2 {
				if k, ok := pairs[i].(string); ok {
					m[k] = pairs[i+1]
				}
			}
			return m
		},

		// query rebuilds the grid URL with one filter changed and the rest
		// preserved, so chips compose instead of resetting each other. Passing
		// the value a filter already holds clears it, which makes every chip a
		// toggle without needing a second helper.
		"query": func(q Query, key, value string) template.URL {
			return template.URL(q.URL(key, value))
		},
		// toggleTag adds or removes one tag, leaving the other tags in place.
		"toggleTag": func(q Query, tag string) template.URL {
			return template.URL(q.WithTagToggled(tag))
		},
		"toggleIO": func(q Query, name string) template.URL {
			return template.URL(q.WithInterfaceToggled(name))
		},
		"hasIO": func(q Query, name string) bool {
			for _, i := range q.Interfaces {
				if strings.EqualFold(i, name) {
					return true
				}
			}
			return false
		},
		"ioIcon": IconForInterface,
		"slice":  func(v ...string) []string { return v },
		"hasTag": func(q Query, tag string) bool {
			for _, t := range q.Tags {
				if strings.EqualFold(t, tag) {
					return true
				}
			}
			return false
		},

		// initials is the placeholder shown for items with no photo yet.
		"initials": func(name string) string {
			var out []rune
			for _, word := range strings.Fields(name) {
				out = append(out, []rune(strings.ToUpper(word))[0])
				if len(out) == 2 {
					break
				}
			}
			return string(out)
		},
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}
