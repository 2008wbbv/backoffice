package main

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"log"
	"net/http"
	"net/url"
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
	Addr     string
	DataDir  string
	Password string
	Title    string
}

func configFromEnv() Config {
	c := Config{
		Addr:     env("PORT", "8080"),
		DataDir:  env("DATA_DIR", "./data"),
		Password: os.Getenv("AUTH_PASSWORD"),
		Title:    env("SITE_TITLE", "Parts Bin"),
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
	cfg    Config
	store  *Store
	photos *PhotoStore
	tmpl   *template.Template
	auth   *Auth
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

	app := &App{
		cfg:    cfg,
		store:  &Store{db: db},
		photos: photos,
		tmpl:   mustTemplates(),
		auth:   auth,
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
		log.Printf("%s listening on http://localhost%s  data=%s  auth=%s", cfg.Title, cfg.Addr, cfg.DataDir, mode)
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
	protected.HandleFunc("GET /{$}", a.handleIndex)
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
	protected.HandleFunc("GET /export.csv", a.handleExportCSV)

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
		"add": func(a, b int) int { return a + b },

		// query rebuilds the grid URL with one filter changed and the rest
		// preserved, so chips compose instead of resetting each other.
		"query": func(q Query, key, value string) template.URL {
			vals := url.Values{}
			set := func(k, v string) {
				if k == key {
					v = value
				}
				if v != "" && !(k == "sort" && v == "recent") {
					vals.Set(k, v)
				}
			}
			set("q", q.Search)
			set("category", q.Category)
			set("location", q.Location)
			set("sort", q.Sort)
			if len(vals) == 0 {
				return "/"
			}
			return template.URL("/?" + vals.Encode())
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
