package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/html"
)

const (
	maxPageBytes = 2 << 20 // enough for any product page's <head>
	fetchTimeout = 15 * time.Second
	maxRedirects = 5
	userAgent    = "Backoffice/1.0 (self-hosted inventory; +https://github.com/2008wbbv/backoffice)"
)

// Fetcher pulls pages and images from the internet on the server's behalf.
//
// That makes it a request-forgery tool if left unguarded: this process usually
// sits inside a home network, so a pasted URL could otherwise reach a router
// admin page, a NAS, or a cloud metadata endpoint that the person pasting it
// cannot reach themselves. Every connection is therefore checked against the
// address it actually resolved to, on every redirect hop.
type Fetcher struct {
	client       *http.Client
	allowPrivate bool
}

func NewFetcher(allowPrivate bool) *Fetcher {
	f := &Fetcher{allowPrivate: allowPrivate}

	dialer := &net.Dialer{
		Timeout: 8 * time.Second,
		// Control runs after DNS resolution with the address actually being
		// dialled, so a hostname that resolves to a private IP -- including one
		// that only does so on the second lookup -- is still blocked.
		Control: func(network, address string, _ syscall.RawConn) error {
			return f.checkAddr(network, address)
		},
	}
	f.client = &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   8 * time.Second,
			ResponseHeaderTimeout: 8 * time.Second,
			DisableKeepAlives:     true,
			Proxy:                 http.ProxyFromEnvironment,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing to follow a %s redirect", req.URL.Scheme)
			}
			return nil
		},
	}
	return f
}

func (f *Fetcher) checkAddr(network, address string) error {
	if !strings.HasPrefix(network, "tcp") {
		return fmt.Errorf("refusing to dial %s", network)
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("bad address %q", address)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("bad address %q", address)
	}
	if f.allowPrivate {
		return nil
	}
	if !isPublicIP(ip) {
		return fmt.Errorf("refusing to fetch from private address %s "+
			"(set ALLOW_PRIVATE_FETCH=1 to permit LAN sources)", ip)
	}
	return nil
}

// isPublicIP rejects everything a hosted service should never be talked into
// reaching: loopback, LAN ranges, link-local (cloud metadata lives at
// 169.254.169.254), carrier NAT, and IPv6 equivalents.
func isPublicIP(ip netip.Addr) bool {
	if ip.Is4In6() {
		ip = ip.Unmap()
	}
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() ||
		ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if ip.Is4() {
		b := ip.As4()
		switch {
		case b[0] == 100 && b[1] >= 64 && b[1] <= 127: // 100.64/10 carrier NAT
			return false
		case b[0] == 192 && b[1] == 0 && b[2] == 0: // 192.0.0/24 protocol assignments
			return false
		case b[0] >= 240: // 240/4 reserved, includes broadcast
			return false
		}
		return true
	}
	// fc00::/7 unique-local, and 64:ff9b::/96 NAT64 which maps back to IPv4.
	if ip.Is6() {
		b := ip.As16()
		if b[0]&0xfe == 0xfc {
			return false
		}
		if b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b {
			return false
		}
	}
	return true
}

// parseTarget validates a user-supplied URL before any connection is attempted.
func parseTarget(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("no URL given")
	}
	// A bare domain is what people actually paste.
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("that does not look like a URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("only http and https URLs are supported")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("that URL has no host")
	}
	return u, nil
}

// PageMeta is what a product page, datasheet listing or wiki article gives us
// to prefill an item with.
type PageMeta struct {
	Title       string
	Description string
	ImageURL    string
	SiteName    string
	Value       string // price or spec, when the page states one
	PartNumber  string
	URL         string
}

// FetchPage downloads a page and reads its metadata. It prefers OpenGraph tags,
// which nearly every shop and wiki emits, and falls back to plain HTML.
func (f *Fetcher) FetchPage(ctx context.Context, raw string) (*PageMeta, error) {
	u, err := parseTarget(raw)
	if err != nil {
		return nil, err
	}
	body, finalURL, err := f.get(ctx, u.String(), "text/html,application/xhtml+xml", maxPageBytes)
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("could not read that page")
	}

	meta, title := collectMeta(doc)
	pm := &PageMeta{URL: finalURL}
	pm.Title = firstOf(meta, "og:title", "twitter:title", "title")
	if pm.Title == "" {
		pm.Title = title
	}
	pm.Description = firstOf(meta, "og:description", "twitter:description", "description")
	pm.SiteName = firstOf(meta, "og:site_name", "application-name")
	pm.PartNumber = firstOf(meta, "product:retailer_item_id", "product:mfr_part_no", "sku", "mpn")

	if price := firstOf(meta, "product:price:amount", "og:price:amount", "twitter:data1"); price != "" {
		currency := firstOf(meta, "product:price:currency", "og:price:currency")
		pm.Value = strings.TrimSpace(currency + " " + price)
	}

	if img := firstOf(meta, "og:image:secure_url", "og:image", "twitter:image", "twitter:image:src"); img != "" {
		base, err := url.Parse(finalURL)
		if err == nil {
			if abs, err := base.Parse(img); err == nil {
				pm.ImageURL = abs.String()
			}
		}
	}

	pm.Title = tidy(pm.Title, 200)
	pm.Description = tidy(pm.Description, 600)
	pm.Value = tidy(pm.Value, 60)
	pm.PartNumber = tidy(pm.PartNumber, 80)
	pm.SiteName = tidy(pm.SiteName, 80)

	if pm.Title == "" && pm.ImageURL == "" {
		return nil, fmt.Errorf("that page had no title or image to import")
	}
	return pm, nil
}

// FetchImage downloads an image URL, returning the raw bytes for PhotoStore to
// validate and thumbnail. It does not trust the served content type; the image
// decoder is the real check.
func (f *Fetcher) FetchImage(ctx context.Context, raw string) ([]byte, error) {
	u, err := parseTarget(raw)
	if err != nil {
		return nil, err
	}
	body, _, err := f.get(ctx, u.String(), "image/*", maxPhotoSize)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("that URL returned an empty response")
	}
	return body, nil
}

func (f *Fetcher) get(ctx context.Context, target, accept string, limit int64) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "en")

	res, err := f.client.Do(req)
	if err != nil {
		return nil, "", cleanFetchError(err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, "", fmt.Errorf("%s returned HTTP %d", req.URL.Host, res.StatusCode)
	}
	// limit+1 so an oversized body is detected rather than silently truncated.
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, "", cleanFetchError(err)
	}
	if int64(len(body)) > limit {
		return nil, "", fmt.Errorf("that response is larger than %d MiB", limit>>20)
	}
	return body, res.Request.URL.String(), nil
}

// cleanFetchError unwraps net/http's layered errors so the browser shows the
// reason (usually the private-address refusal) rather than a URL dump.
func cleanFetchError(err error) error {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i > 0 {
		if tail := msg[i+2:]; strings.Contains(tail, "refusing") || strings.Contains(tail, "redirect") {
			return fmt.Errorf("%s", tail)
		}
	}
	// A raw dial error names the resolver and socket, which tells the person
	// pasting a link nothing useful.
	var dns *net.DNSError
	if errors.As(err, &dns) {
		if dns.IsNotFound {
			return fmt.Errorf("no such domain: %s", dns.Name)
		}
		return fmt.Errorf("could not look up %s", dns.Name)
	}
	var ue *url.Error
	if ok := asURLError(err, &ue); ok {
		if ue.Timeout() {
			return fmt.Errorf("that site took too long to respond")
		}
		return fmt.Errorf("could not reach %s", ue.URL)
	}
	return err
}

func asURLError(err error, target **url.Error) bool {
	for err != nil {
		if ue, ok := err.(*url.Error); ok {
			*target = ue
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// collectMeta indexes every <meta> by its name or property, plus <title>.
func collectMeta(doc *html.Node) (map[string]string, string) {
	meta := map[string]string{}
	var title string

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "meta":
				var key, content string
				for _, a := range n.Attr {
					switch strings.ToLower(a.Key) {
					case "property", "name", "itemprop":
						if key == "" {
							key = strings.ToLower(strings.TrimSpace(a.Val))
						}
					case "content":
						content = a.Val
					}
				}
				if key != "" && content != "" && meta[key] == "" {
					meta[key] = content
				}
			case "title":
				if title == "" && n.FirstChild != nil {
					title = n.FirstChild.Data
				}
			case "body":
				return // everything worth reading is in <head>
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return meta, title
}

func firstOf(meta map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(meta[k]); v != "" {
			return v
		}
	}
	return ""
}

// tidy collapses whitespace and trims to a length that fits the form fields.
func tidy(s string, max int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > max {
		// Cut on a rune boundary, and prefer the last word break.
		s = s[:max]
		if i := strings.LastIndexAny(s, " ,;"); i > max/2 {
			s = s[:i]
		}
		s = strings.ToValidUTF8(s, "") + "…"
	}
	return s
}
