package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookie = "parts_session"
	sessionMaxAge = 30 * 24 * time.Hour
)

// Auth is an intentionally small gate: one shared password, set with
// AUTH_PASSWORD. Leaving it unset disables auth entirely, which is the right
// default for something already behind a LAN or a tunnel.
type Auth struct {
	password string
	key      []byte
}

func NewAuth(password, keyPath string) (*Auth, error) {
	a := &Auth{password: password}
	if password == "" {
		return a, nil
	}
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	a.key = key
	return a, nil
}

func (a *Auth) Enabled() bool { return a.password != "" }

func loadOrCreateKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	// 0600: this key is what keeps existing sessions valid across restarts.
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write session key: %w", err)
	}
	return key, nil
}

func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Enabled() || a.valid(r) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "session expired, reload and sign in again", http.StatusForbidden)
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	})
}

func (a *Auth) valid(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	value, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	expires, err := strconv.ParseInt(value, 10, 64)
	if err != nil || time.Now().Unix() > expires {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(a.sign(value)))
}

func (a *Auth) sign(value string) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(value))
	return hex.EncodeToString(m.Sum(nil))
}

func (a *Auth) issue(w http.ResponseWriter, r *http.Request) {
	value := strconv.FormatInt(time.Now().Add(sessionMaxAge).Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value + "." + a.sign(value),
		Path:     "/",
		HttpOnly: true,
		// Lax is what keeps a third-party page from POSTing edits through your
		// browser; the app has no cross-site flows that need None.
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge:   int(sessionMaxAge / time.Second),
	})
}

func (a *Auth) clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
}

// Check compares in constant time so a wrong password leaks no timing signal.
func (a *Auth) Check(attempt string) bool {
	return subtle.ConstantTimeCompare([]byte(attempt), []byte(a.password)) == 1
}
