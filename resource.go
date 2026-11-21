package main

// Pictures for the widgets (addon.Resourcer): Proclaim's slide images,
// stage (confidence) images and the presentation thumbnail.
//
//	slide/<itemId>/<index>.png?w=640&rev=<localRevision>
//	confidence/<itemId>/<index>.png?w=640&rev=<localRevision>
//	thumbnail.png?w=320
//
// index is 0-based; rev is the slide's localRevision from the service
// (it changes when the item is edited, so a new picture is fetched).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/toolingdev/jadeck/sdk/addon"
)

const (
	maxWidth     = 1280
	defaultWidth = 640
	cacheEntries = 96
	cacheBytes   = 48 << 20
	thumbTTL     = 5 * time.Second // the thumbnail has no revision of its own
)

var (
	pngMagic = []byte("\x89PNG\r\n\x1a\n")
	guidRe   = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
)

// imageCache keeps recent pictures, oldest out first.
type imageCache struct {
	mu    sync.Mutex
	order []string
	m     map[string]cached
	size  int
}

type cached struct {
	data []byte
	at   time.Time
}

func (ic *imageCache) get(key string, ttl time.Duration) ([]byte, bool) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	e, ok := ic.m[key]
	if !ok || (ttl > 0 && time.Since(e.at) > ttl) {
		return nil, false
	}
	return e.data, true
}

func (ic *imageCache) put(key string, data []byte) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if ic.m == nil {
		ic.m = map[string]cached{}
	}
	if old, ok := ic.m[key]; ok {
		ic.size -= len(old.data)
		for i, k := range ic.order {
			if k == key {
				ic.order = append(ic.order[:i], ic.order[i+1:]...)
				break
			}
		}
	}
	ic.m[key] = cached{data, time.Now()}
	ic.order = append(ic.order, key)
	ic.size += len(data)
	for len(ic.order) > cacheEntries || (ic.size > cacheBytes && len(ic.order) > 1) {
		k := ic.order[0]
		ic.order = ic.order[1:]
		ic.size -= len(ic.m[k].data)
		delete(ic.m, k)
	}
}

func (ic *imageCache) len() int {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return len(ic.order)
}

// picture is a parsed resource path.
type picture struct {
	kind  string // slide | confidence | thumbnail
	item  string
	index int
	width int
	rev   int64
}

func parsePicture(path string) (picture, error) {
	u, err := url.Parse(path)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return picture{}, addon.ErrNotFound
	}
	q := u.Query()
	pic := picture{width: defaultWidth}
	if w := q.Get("w"); w != "" {
		n, err := strconv.Atoi(w)
		if err != nil || n < 16 {
			return picture{}, fmt.Errorf("bad width %q", w)
		}
		pic.width = min(n, maxWidth)
	}
	if r := q.Get("rev"); r != "" {
		if pic.rev, err = strconv.ParseInt(r, 10, 64); err != nil {
			return picture{}, fmt.Errorf("bad revision %q", r)
		}
	}
	seg := strings.Split(u.Path, "/")
	switch {
	case len(seg) == 1 && seg[0] == "thumbnail.png":
		pic.kind = "thumbnail"
	case len(seg) == 3 && (seg[0] == "slide" || seg[0] == "confidence") && guidRe.MatchString(seg[1]) && strings.HasSuffix(seg[2], ".png"):
		n, err := strconv.Atoi(strings.TrimSuffix(seg[2], ".png"))
		if err != nil || n < 0 || n > 100000 {
			return picture{}, addon.ErrNotFound
		}
		pic.kind, pic.item, pic.index = seg[0], seg[1], n
	default:
		return picture{}, addon.ErrNotFound
	}
	return pic, nil
}

// Resource serves a picture the state points at, from Proclaim (cached).
func (c *Conn) Resource(ctx context.Context, path string) (string, []byte, error) {
	pic, err := parsePicture(path)
	if err != nil {
		return "", nil, err
	}
	key := fmt.Sprintf("%s|%s|%d|%d|%d", pic.kind, strings.ToLower(pic.item), pic.index, pic.rev, pic.width)
	ttl := time.Duration(0)
	if pic.kind == "thumbnail" || pic.rev == 0 { // rev 0 = "the latest": can't be cached for good
		ttl = thumbTTL
	}
	if b, ok := c.images.get(key, ttl); ok {
		return "image/png", b, nil
	}
	var route string
	q := url.Values{"width": {strconv.Itoa(pic.width)}, "longPolling": {"true"}}
	if pic.kind == "thumbnail" {
		route = "/presentations/onair/thumbnail?" + q.Encode()
	} else {
		q.Set("localRevision", strconv.FormatInt(pic.rev, 10))
		if pic.kind == "confidence" {
			q.Set("confidence", "true")
		}
		route = fmt.Sprintf("/presentations/onair/items/%s/slides/%d/image?%s", pic.item, pic.index, q.Encode())
	}
	b, err := c.fetchImage(ctx, route)
	if err != nil {
		return "", nil, err
	}
	c.images.put(key, b)
	return "image/png", b, nil
}

// fetchImage gets a PNG from Proclaim. While it renders, Proclaim answers
// 202 {completionEstimateMs} (even with longPolling, at times): wait and
// ask again, for up to 20 s.
func (c *Conn) fetchImage(ctx context.Context, route string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c.mu.Lock()
	sess := c.sess
	if c.watched.sess != "" {
		sess = c.watched.sess
	}
	c.mu.Unlock()
	for attempt := 0; ; attempt++ {
		code, b, err := c.send(ctx, c.poll, "GET", route, nil, map[string]string{"OnAirSessionId": sess})
		if err != nil {
			return nil, err
		}
		switch code {
		case 200:
			if !bytes.HasPrefix(b, pngMagic) {
				return nil, fmt.Errorf("Proclaim sent a picture that isn't a PNG")
			}
			if len(b) > addon.MaxResource {
				return nil, fmt.Errorf("Proclaim's picture is too big (%d bytes); ask for a smaller width", len(b))
			}
			return b, nil
		case 202:
			var st struct {
				Ms int `json:"completionEstimateMs"`
			}
			json.Unmarshal(b, &st)
			wait := time.Duration(min(max(st.Ms, 100), 2000)) * time.Millisecond
			if attempt >= 30 || !sleep(ctx, wait) {
				return nil, fmt.Errorf("Proclaim is still drawing that slide")
			}
		case 404:
			if e := apiError(b); e == " (PresentationNotOnAir)" {
				return nil, errOffAir
			}
			return nil, addon.ErrNotFound
		default:
			return nil, fmt.Errorf("Proclaim answered %d to a picture%s", code, apiError(b))
		}
	}
}

var _ addon.Resourcer = (*Conn)(nil)
