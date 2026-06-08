package letterboxd

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	xhtml "golang.org/x/net/html"
)

const (
	defaultMaxItems = 1000
	maxPages        = 20
)

var titleYearPattern = regexp.MustCompile(`\s+\((\d{4})\)$`)
var listCountPattern = regexp.MustCompile(`(?i)\ba list of ([\d,]+) films?\b`)

// Client reads public Letterboxd list pages. It intentionally only uses the list
// HTML and avoids Letterboxd's per-film JSON endpoints, which can be challenged.
type Client struct {
	mu         sync.RWMutex
	httpClient *http.Client
	cacheTTL   time.Duration
	cache      map[string]cacheEntry
}

type cacheEntry struct {
	items     []ListItem
	total     int
	complete  bool
	expiresAt time.Time
}

// ListItem is a normalized public Letterboxd list entry.
type ListItem struct {
	Title     string
	Year      int
	MediaType string
	Slug      string
	URL       string
}

// ListResult contains parsed list entries and the source list's reported item count.
type ListResult struct {
	Items []ListItem
	Total int
}

// NewClient creates a public Letterboxd list client.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 20 * time.Second},
		cacheTTL:   30 * time.Minute,
		cache:      make(map[string]cacheEntry),
	}
}

// SetHTTPClientForTest overrides the HTTP client.
func (c *Client) SetHTTPClientForTest(httpClient *http.Client) {
	if httpClient != nil {
		c.httpClient = httpClient
	}
}

// GetListItems returns movie entries from a public Letterboxd list URL.
func (c *Client) GetListItems(ctx context.Context, rawURL string, maxItems int) ([]ListItem, error) {
	result, err := c.GetListResult(ctx, rawURL, maxItems)
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

// GetListResult returns movie entries and the source list's reported total count.
func (c *Client) GetListResult(ctx context.Context, rawURL string, maxItems int) (ListResult, error) {
	canonicalURL, err := normalizeListURL(rawURL)
	if err != nil {
		return ListResult{}, err
	}
	if maxItems <= 0 || maxItems > defaultMaxItems {
		maxItems = defaultMaxItems
	}

	if result, ok := c.getCached(canonicalURL, maxItems); ok {
		return result, nil
	}

	result, err := c.fetchListItems(ctx, canonicalURL, maxItems)
	if err != nil {
		return ListResult{}, err
	}
	c.setCached(canonicalURL, result)
	if len(result.Items) > maxItems {
		result.Items = result.Items[:maxItems]
	}
	return result, nil
}

func (c *Client) fetchListItems(ctx context.Context, firstURL string, maxItems int) (ListResult, error) {
	seen := make(map[string]bool)
	items := make([]ListItem, 0, maxItems)
	nextURL := firstURL
	total := 0

	for page := 0; page < maxPages && nextURL != ""; page++ {
		doc, err := c.fetchHTML(ctx, nextURL)
		if err != nil {
			return ListResult{}, err
		}
		pageItems, pageNext, pageTotal := parseListPage(doc, nextURL)
		if pageTotal > total {
			total = pageTotal
		}
		for _, item := range pageItems {
			key := item.URL
			if key == "" {
				key = strings.ToLower(item.Title) + ":" + strconv.Itoa(item.Year)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			items = append(items, item)
			if len(items) >= maxItems {
				if total < len(items) {
					total = len(items)
				}
				return ListResult{Items: items, Total: total}, nil
			}
		}
		if pageNext == "" || pageNext == nextURL || len(pageItems) == 0 {
			break
		}
		nextURL = pageNext
	}
	if total < len(items) {
		total = len(items)
	}
	return ListResult{Items: items, Total: total}, nil
}

func (c *Client) fetchHTML(ctx context.Context, endpoint string) (*xhtml.Node, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create letterboxd request: %w", err)
	}
	req.Header.Set("User-Agent", "mediastorm/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("letterboxd request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("letterboxd returned %d: %s", resp.StatusCode, string(body))
	}
	doc, err := xhtml.Parse(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("parse letterboxd html: %w", err)
	}
	if pageTitle := strings.ToLower(strings.TrimSpace(findTitleText(doc))); strings.Contains(pageTitle, "just a moment") {
		return nil, fmt.Errorf("letterboxd challenge page returned")
	}
	return doc, nil
}

func (c *Client) getCached(canonicalURL string, maxItems int) (ListResult, bool) {
	c.mu.RLock()
	entry, ok := c.cache[canonicalURL]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return ListResult{}, false
	}
	if !entry.complete && maxItems > len(entry.items) {
		return ListResult{}, false
	}
	items := append([]ListItem(nil), entry.items...)
	if len(items) > maxItems {
		items = items[:maxItems]
	}
	return ListResult{Items: items, Total: entry.total}, true
}

func (c *Client) setCached(canonicalURL string, result ListResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[canonicalURL] = cacheEntry{
		items:     append([]ListItem(nil), result.Items...),
		total:     result.Total,
		complete:  result.Total <= len(result.Items),
		expiresAt: time.Now().Add(c.cacheTTL),
	}
}

func normalizeListURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("letterboxd list url required")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse letterboxd url: %w", err)
	}
	if u.Scheme == "" {
		u.Scheme = "https"
	}
	host := strings.ToLower(u.Hostname())
	if host != "letterboxd.com" && host != "www.letterboxd.com" {
		return "", fmt.Errorf("letterboxd url must be on letterboxd.com")
	}
	path := strings.Trim(u.Path, "/")
	if !strings.Contains(path, "/list/") && !isWatchlistPath(path) {
		return "", fmt.Errorf("letterboxd url must be a public list or watchlist url")
	}
	u.Scheme = "https"
	u.Host = "letterboxd.com"
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	return u.String(), nil
}

func isWatchlistPath(path string) bool {
	parts := strings.Split(path, "/")
	return len(parts) >= 2 && parts[1] == "watchlist"
}

func parseListPage(doc *xhtml.Node, pageURL string) ([]ListItem, string, int) {
	items := make([]ListItem, 0)
	var nextHref string
	total := 0

	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			if n.Data == "meta" {
				if name := strings.ToLower(attr(n, "name")); name == "description" {
					total = maxInt(total, parseListCount(attr(n, "content")))
				}
			}
			if name := attr(n, "data-item-name"); name != "" {
				if item, ok := normalizeListItem(name, attr(n, "data-item-slug"), attr(n, "data-target-link"), pageURL); ok {
					items = append(items, item)
				}
			}
			if n.Data == "a" && nextHref == "" && isNextLink(n) {
				nextHref = resolveURL(pageURL, attr(n, "href"))
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)

	return items, nextHref, total
}

func parseListCount(description string) int {
	matches := listCountPattern.FindStringSubmatch(html.UnescapeString(description))
	if len(matches) != 2 {
		return 0
	}
	count, _ := strconv.Atoi(strings.ReplaceAll(matches[1], ",", ""))
	return count
}

func normalizeListItem(name, slug, targetLink, pageURL string) (ListItem, bool) {
	title, year := splitTitleYear(html.UnescapeString(name))
	if title == "" {
		return ListItem{}, false
	}
	itemURL := resolveURL(pageURL, targetLink)
	if slug == "" && itemURL != "" {
		if u, err := url.Parse(itemURL); err == nil {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) >= 2 && parts[0] == "film" {
				slug = parts[1]
			}
		}
	}
	return ListItem{
		Title:     title,
		Year:      year,
		MediaType: "movie",
		Slug:      slug,
		URL:       itemURL,
	}, true
}

func splitTitleYear(name string) (string, int) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\u200e", ""))
	matches := titleYearPattern.FindStringSubmatch(name)
	if len(matches) != 2 {
		return name, 0
	}
	year, _ := strconv.Atoi(matches[1])
	title := strings.TrimSpace(strings.TrimSuffix(name, matches[0]))
	return title, year
}

func isNextLink(n *xhtml.Node) bool {
	href := attr(n, "href")
	if href == "" {
		return false
	}
	rel := strings.ToLower(attr(n, "rel"))
	className := strings.ToLower(attr(n, "class"))
	text := strings.ToLower(strings.TrimSpace(nodeText(n)))
	return strings.Contains(rel, "next") ||
		strings.Contains(className, "next") ||
		text == "next"
}

func resolveURL(baseURL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return href
	}
	ref, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(ref).String()
}

func attr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func findTitleText(doc *xhtml.Node) string {
	var found string
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if found != "" {
			return
		}
		if n.Type == xhtml.ElementNode && n.Data == "title" {
			found = nodeText(n)
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return found
}

func nodeText(n *xhtml.Node) string {
	var b strings.Builder
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.TextNode {
			b.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
