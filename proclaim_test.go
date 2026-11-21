package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProclaim is a small Proclaim Local Remote server (4.22, 1.4.0.0)
// following docs/PROCLAIM-API.md.
type fakeProclaim struct {
	t *testing.T
	*httptest.Server

	mu        sync.Mutex
	changed   *sync.Cond
	sess      string // "" = off air
	items     []fakeItem
	warmup    int // warmupStartIndex
	start     int // serviceStartIndex
	post      int // postServiceStartIndex
	presRev   int64
	statusRev int64 // presentationLocalRevision in the status (can differ after a forced update)
	step      int64
	item      int
	slide     int
	qsKind    string
	qsData    string
	media     string

	noControl  bool // /auth/control answers 500
	ignorePost bool // POST statusChanged answers 200 but changes nothing

	cmds       []string // app commands run
	posts      int
	presGets   int
	userAgents map[string]bool
	polls      []string // query + ConnectionId of status reads
	disconnect int
	images     []string // image requests: path?query
	rendered   map[string]bool
}

type fakeItem struct {
	id, title, kind string
	slides          int
	notes           string
}

// tinyPNG is a 1×1 PNG.
var tinyPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

const (
	customQS    = "30000000-0000-0000-0000-000000000001"
	customQSFor = "10000000-0000-0000-0000-000000000006"
)

const fakeSession = "0123456789abcdef0123456789abcdef"

func newFake(t *testing.T, onAir bool) *fakeProclaim {
	f := &fakeProclaim{t: t, userAgents: map[string]bool{},
		items: []fakeItem{
			{"10000000-0000-0000-0000-000000000001", "Countdown", "Video", 1, ""},                                        // pre-service
			{"10000000-0000-0000-0000-000000000002", "Welcome", "Content", 1, ""},                                        // warm-up
			{"10000000-0000-0000-0000-000000000003", "Opening Song", "SongLyrics", 4, "<p>Key of <b>G</b>&amp;slow</p>"}, // service: GoToServiceItem 1
			{"10000000-0000-0000-0000-000000000004", "Start timer", "ConfidenceTimerCue", 0, ""},
			{"10000000-0000-0000-0000-000000000005", "Reading", "BiblePassage", 2, ""}, // GoToServiceItem 2
			{"10000000-0000-0000-0000-000000000006", "Thank You", "Content", 1, ""},    // post-service, GoToServiceItem 3
		},
		rendered: map[string]bool{},
		warmup:   1, start: 2, post: 5, presRev: 7, statusRev: 7, step: 3, item: 2, slide: 1, qsKind: "None"}
	if onAir {
		f.sess = fakeSession
	}
	f.changed = sync.NewCond(&f.mu)
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	notOnAir := func(w http.ResponseWriter) { j(w, 404, map[string]string{"errorCode": "PresentationNotOnAir"}) }
	// session checks OnAirSessionId (callers hold f.mu).
	session := func(w http.ResponseWriter, r *http.Request) bool {
		if f.sess == "" || r.Header.Get("OnAirSessionId") != f.sess {
			notOnAir(w)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write([]byte("Hello from Proclaim Local Remote version 1.4.0.0"))
	})
	mux.HandleFunc("GET /onair/session", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.userAgents[r.UserAgent()] = true
		if f.sess == "" {
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write([]byte(f.sess))
	})
	mux.HandleFunc("POST /auth/control", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !session(w, r) {
			return
		}
		if f.noControl {
			j(w, 500, map[string]string{"errorCode": "Unknown"})
			return
		}
		var b struct{ UserName, RemoteDeviceName, Password string }
		json.NewDecoder(r.Body).Decode(&b)
		if b.Password != "Proclaim" || b.UserName == "" {
			j(w, 403, map[string]string{"errorCode": "AccessDenied"})
			return
		}
		j(w, 200, map[string]string{"authenticationToken": "ctl-token", "connectionId": "conn-1"})
	})
	mux.HandleFunc("GET /auth/disconnectcontrol", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if session(w, r) && r.Header.Get("ConnectionId") == "conn-1" {
			f.disconnect++
		}
	})
	mux.HandleFunc("GET /onair/statusChanged", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !session(w, r) {
			return
		}
		q := r.URL.Query()
		f.polls = append(f.polls, q.Encode()+"|"+r.Header.Get("ConnectionId"))
		if q.Has("localRevision") && q.Has("step") {
			// Long poll: hold while both match (2 s here instead of 60 s).
			deadline := time.Now().Add(2 * time.Second)
			go func() { time.Sleep(2 * time.Second); f.mu.Lock(); f.changed.Broadcast(); f.mu.Unlock() }()
			for q.Get("localRevision") == strconv.FormatInt(f.statusRev, 10) && q.Get("step") == strconv.FormatInt(f.step, 10) &&
				f.sess != "" && time.Now().Before(deadline) {
				f.changed.Wait()
			}
			if f.sess == "" {
				notOnAir(w)
				return
			}
		}
		j(w, 200, f.status())
	})
	mux.HandleFunc("POST /onair/statusChanged", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !session(w, r) {
			return
		}
		if r.Header.Get("ProclaimAuthToken") != "ctl-token" {
			j(w, 403, map[string]string{"errorCode": "AccessDenied"})
			return
		}
		var b statusUpdate
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			j(w, 500, map[string]string{"errorCode": "Unknown"})
			return
		}
		f.posts++
		if b.Status.Revision < f.step || f.ignorePost {
			return // old step: ignored
		}
		idx := -1
		for i, it := range f.items {
			if it.id == b.Status.ItemID {
				idx = i
			}
		}
		if idx < 0 {
			j(w, 404, map[string]string{"errorCode": "ItemNotFound"})
			return
		}
		f.item, f.slide, f.qsKind = idx, b.Status.SlideIndex, b.Status.QuickScreenKind
		f.qsData, f.media = "", ""
		if b.Status.QuickScreenData != nil {
			f.qsData = *b.Status.QuickScreenData
		}
		if b.Status.MediaState != nil {
			f.media = *b.Status.MediaState
		}
		f.bump()
	})
	mux.HandleFunc("GET /presentations/onair", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !session(w, r) {
			return
		}
		f.presGets++
		items := []map[string]any{}
		for _, it := range f.items {
			sl := []map[string]any{}
			for i := 0; i < it.slides; i++ {
				sl = append(sl, map[string]any{"localRevision": 1, "index": i})
			}
			items = append(items, map[string]any{"id": it.id, "title": it.title, "notes": it.notes, "kind": it.kind, "slides": sl})
		}
		var qs any // null unless the client says it is Proclaim's own remote
		if r.UserAgent() == "ProclaimLocalRemote/1.4.0.0" {
			qs = []any{
				map[string]any{"id": "30000000-0000-0000-0000-0000000000aa", "serviceItemId": nil, "kind": "Blank", "title": nil},
				map[string]any{"id": customQS, "serviceItemId": customQSFor, "kind": "Custom", "title": "Prayer Request"},
			}
		}
		j(w, 200, map[string]any{"sessionId": f.sess, "id": "20000000-0000-0000-0000-000000000000", "localRevision": f.presRev,
			"title": "Weekend Service", "warmupStartIndex": f.warmup, "serviceStartIndex": f.start, "postServiceStartIndex": f.post,
			"startTime": int64(638000000000000000), "serviceItems": items, "quickScreens": qs})
	})
	// Pictures: the first request for each one answers 202 while it
	// "renders", like Proclaim does for a slide it hasn't drawn yet.
	picture := func(w http.ResponseWriter, key string) {
		if !f.rendered[key] {
			f.rendered[key] = true
			j(w, 202, map[string]int{"completionEstimateMs": 10})
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(tinyPNG)
	}
	mux.HandleFunc("GET /presentations/onair/items/{id}/slides/{idx}/image", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.images = append(f.images, r.URL.Path+"?"+r.URL.RawQuery)
		q := r.URL.Query()
		if f.sess == "" {
			notOnAir(w)
			return
		}
		n, err := strconv.Atoi(r.PathValue("idx"))
		found := false
		for _, it := range f.items {
			if strings.EqualFold(it.id, r.PathValue("id")) && err == nil && n >= 0 && n < it.slides {
				found = true
			}
		}
		if !found || !q.Has("localRevision") || !q.Has("width") {
			j(w, 404, map[string]string{"errorCode": "ItemNotFound"})
			return
		}
		picture(w, r.URL.Path+"|"+q.Get("confidence")+"|"+q.Get("width"))
	})
	mux.HandleFunc("GET /presentations/onair/thumbnail", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.images = append(f.images, r.URL.Path+"?"+r.URL.RawQuery)
		if f.sess == "" {
			notOnAir(w)
			return
		}
		picture(w, "thumb")
	})
	mux.HandleFunc("POST /appCommand/authenticate", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Password string }
		json.NewDecoder(r.Body).Decode(&b)
		switch b.Password {
		case "":
			w.WriteHeader(400)
		case "secret":
			j(w, 200, map[string]string{"proclaimAuthToken": "app-token"})
		default:
			w.WriteHeader(403)
		}
	})
	mux.HandleFunc("GET /appCommand/perform", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ProclaimAuthToken") != "app-token" { // the fake is never "the same machine"
			w.WriteHeader(401)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		name := r.URL.Query().Get("appCommandName")
		idx, _ := strconv.Atoi(r.URL.Query().Get("index"))
		if !f.perform(name, idx) {
			w.WriteHeader(404)
			return
		}
		f.cmds = append(f.cmds, name+":"+r.URL.Query().Get("index"))
		w.Write([]byte("success"))
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(func() {
		f.mu.Lock()
		f.sess = "" // release held polls
		f.changed.Broadcast()
		f.mu.Unlock()
		f.Close()
	})
	return f
}

func (f *fakeProclaim) status() statusUpdate {
	st := statusUpdate{PresentationID: "20000000-0000-0000-0000-000000000000", PresentationLocalRevision: f.statusRev,
		Status: onAirStatus{Revision: f.step, ItemID: f.items[f.item].id, SlideIndex: f.slide, QuickScreenKind: f.qsKind}}
	if f.qsData != "" {
		st.Status.QuickScreenData = &f.qsData
	}
	if f.media != "" {
		st.Status.MediaState = &f.media
	}
	return st
}

func (f *fakeProclaim) bump() { f.step++; f.changed.Broadcast() }

// perform runs an app command the way Proclaim does (callers hold f.mu).
func (f *fakeProclaim) perform(name string, idx int) bool {
	switch strings.ToLower(name) {
	case "nextslide":
		if f.slide+1 < f.items[f.item].slides {
			f.slide++
		}
	case "previousslide":
		f.slide = max(f.slide-1, 0)
	case "gotoserviceitem": // counts from the service start, skipping items with no slides
		k := 0
		for i := f.start; i < len(f.items); i++ {
			if f.items[i].slides > 0 {
				if k++; k == idx {
					f.item, f.slide, f.qsKind = i, 0, "None"
					break
				}
			}
		}
	case "gotoslide":
		if idx >= 1 && idx <= f.items[f.item].slides {
			f.slide = idx - 1
		}
	case "showblankquickscreen":
		f.qsKind = "Blank"
	case "showamenquickscreen":
		f.qsKind = "Amen"
	case "showcustomquickscreen":
		if idx != 1 {
			return true // Proclaim ignores an index it doesn't have
		}
		f.qsKind, f.qsData = "Custom", customQS
	case "showsonglyricsversebyindex", "showsonglyricschorusbyindex":
		// The fake's song: V1 V2 C V3 (slides 0-3); no such part, no move.
		at := map[string][]int{"showsonglyricsversebyindex": {0, 1, 3}, "showsonglyricschorusbyindex": {2}}[strings.ToLower(name)]
		if f.items[f.item].kind != "SongLyrics" || idx < 1 || idx > len(at) || f.slide == at[idx-1] {
			return true
		}
		f.slide = at[idx-1]
	case "showsonglyricsbridgebyindex", "showsonglyricstagbyindex", "showsonglyricsprechorusbyindex",
		"showsonglyricsendingbyindex", "showsonglyricsinterludebyindex":
		return true // the fake's song has none: Proclaim stays put
	case "videorestart", "videorewind", "videofastforward", "nextaudioitem":
	case "shownotextquickscreen":
		f.qsKind = "NoText"
	case "showtext", "showlastslide":
		f.qsKind, f.qsData = "None", ""
	case "startwarmup":
		f.item, f.slide = f.warmup, 0
	case "startpostservice":
		f.item, f.slide = f.post, 0
	case "videoplay":
		f.media = "Playing"
	case "videopause":
		f.media = "Paused"
	case "nextserviceitem", "previousserviceitem", "showlogoquickscreen", "startservice", "startpreservice", "goonair", "gooffair":
	default:
		return false
	}
	f.bump()
	return true
}

// counts reads what the fake recorded.
func (f *fakeProclaim) counts() (cmds string, posts, presGets, disconnect int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.cmds, ","), f.posts, f.presGets, f.disconnect
}

func (f *fakeProclaim) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *fakeProclaim) at() (item, slide int, qs, media string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.item, f.slide, f.qsKind, f.media
}

func open(t *testing.T, f *fakeProclaim, pw string) *Conn {
	u, _ := url.Parse(f.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	c, err := Open(json.RawMessage(`{"host":"` + host + `","password":"` + pw + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	c.base = "http://" + net.JoinHostPort(host, port) // the fake isn't on 52195
	c.local = false                                   // exercise the login path
	c.confirm = 300 * time.Millisecond
	c.retry = 50 * time.Millisecond
	t.Cleanup(func() { c.Close() })
	return c
}

func TestCommandsLogIn(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	ctx := context.Background()
	if _, err := c.Do(ctx, "next"); err != nil {
		t.Fatal(err)
	}
	if d, err := c.Do(ctx, "black"); err != nil || d["confirmed"] != true {
		t.Fatalf("black: %v %v", d, err)
	}
	if _, err := c.AddonAction(ctx, "start_warm_up", nil); err != nil {
		t.Fatal(err)
	}
	if cmds, _, _, _ := f.counts(); cmds != "NextSlide:,ShowBlankQuickScreen:,StartWarmUp:" {
		t.Fatalf("sent %v", cmds)
	}
	f.set(func() {
		if !f.userAgents["ProclaimLocalRemote/1.4.0.0"] || len(f.userAgents) != 1 {
			t.Errorf("user agents %v", f.userAgents)
		}
	})
	if _, err := c.Do(ctx, "dance"); err == nil {
		t.Fatal("unknown action accepted")
	}
	if _, err := c.AddonAction(ctx, "dance", nil); err == nil {
		t.Fatal("unknown add-on action accepted")
	}
}

func TestWrongPassword(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "nope")
	if _, err := c.Do(context.Background(), "next"); err == nil || !strings.Contains(err.Error(), "refused the network control password") {
		t.Fatalf("want a wrong-password error, got %v", err)
	}
}

func TestStaleTokenLogsInAgain(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	c.token = "old" // e.g. Proclaim restarted since
	if _, err := c.Do(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	if c.token != "app-token" {
		t.Fatalf("token %q", c.token)
	}
}

func TestState(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	st, err := c.AddonState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st["onAir"] != true || st["currentIndex"] != 3 || st["itemTitle"] != "Opening Song" || st["slide"] != 2 || st["slides"] != 4 || st["section"] != "service" {
		t.Fatalf("state %v", st)
	}
	items := st["items"].([]map[string]any)
	if len(items) != 6 || items[0]["section"] != "pre-service" || items[1]["section"] != "warm-up" || items[5]["section"] != "post-service" {
		t.Fatalf("items %v", items)
	}
	off := newFake(t, false)
	st, _ = open(t, off, "secret").AddonState(context.Background())
	if st["onAir"] != false || st["reachable"] != true {
		t.Fatalf("off air state %v", st)
	}
}

// Revisions are .NET ticks in real life: far beyond int32.
func TestBigRevisions(t *testing.T) {
	f := newFake(t, true)
	f.presRev, f.statusRev, f.step = 638700000000000000, 638700000000000000, 638700000000000001
	c := open(t, f, "secret")
	if st, err := c.AddonState(context.Background()); err != nil || st["currentIndex"] != 3 {
		t.Fatalf("state %v %v", st, err)
	}
	if _, err := c.AddonAction(context.Background(), "go_to_item", json.RawMessage(`{"index":5}`)); err != nil {
		t.Fatal(err)
	}
}

// After a forced update the status's revision can stay different from the
// presentation's: fetch the service once, not on every read.
func TestNoEndlessRefetch(t *testing.T) {
	f := newFake(t, true)
	f.statusRev = 9 // the presentation says 7
	c := open(t, f, "secret")
	for range 3 {
		if _, err := c.AddonState(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, n, _ := f.counts(); n != 1 {
		t.Fatalf("fetched the service %d times", n)
	}
	f.set(func() { f.statusRev = 10 }) // edited: fetch again
	c.AddonState(context.Background())
	if _, _, n, _ := f.counts(); n != 2 {
		t.Fatalf("fetched the service %d times after an edit", n)
	}
}

// Item numbers are positions in the list the user sees, pre-service items
// and items without slides included.
func TestGoToItemExact(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	ctx := context.Background()
	for _, n := range []int{1, 5, 6, 2} {
		if _, err := c.AddonAction(ctx, "go_to_item", json.RawMessage(`{"index":`+strconv.Itoa(n)+`}`)); err != nil {
			t.Fatalf("item %d: %v", n, err)
		}
		if item, slide, qs, _ := f.at(); item != n-1 || slide != 0 || qs != "None" {
			t.Fatalf("item %d: Proclaim is at %d/%d %s", n, item, slide, qs)
		}
	}
	if cmds, posts, _, _ := f.counts(); cmds != "" || posts != 4 {
		t.Fatalf("want the remote-control route only: cmds %v, posts %d", cmds, posts)
	}
	if _, err := c.AddonAction(ctx, "go_to_item", json.RawMessage(`{"index":4}`)); err == nil || !strings.Contains(err.Error(), "no slides") {
		t.Fatalf("item without slides: %v", err)
	}
	if _, err := c.AddonAction(ctx, "go_to_item", json.RawMessage(`{"index":9}`)); err == nil {
		t.Fatal("item 9 of 6 accepted")
	}
	if _, err := c.AddonAction(ctx, "go_to_item", json.RawMessage(`{"index":"2"}`)); err == nil {
		t.Fatal("bad params accepted")
	}
}

// Without the remote-control login, go_to_item falls back to
// GoToServiceItem with Proclaim's own numbering.
func TestGoToItemFallback(t *testing.T) {
	f := newFake(t, true)
	f.noControl = true
	c := open(t, f, "secret")
	ctx := context.Background()
	for _, n := range []int{5, 6, 3} {
		res, err := c.AddonAction(ctx, "go_to_item", json.RawMessage(`{"index":`+strconv.Itoa(n)+`}`))
		if err != nil {
			t.Fatalf("item %d: %v", n, err)
		}
		if res.(map[string]any)["via"] != "app command" {
			t.Fatalf("item %d: %v", n, res)
		}
		if item, _, _, _ := f.at(); item != n-1 {
			t.Fatalf("item %d: Proclaim is at %d", n, item)
		}
	}
	if cmds, _, _, _ := f.counts(); cmds != "GoToServiceItem:2,GoToServiceItem:3,GoToServiceItem:1" {
		t.Fatalf("sent %v", cmds)
	}
	if _, err := c.AddonAction(ctx, "go_to_item", json.RawMessage(`{"index":1}`)); err == nil || !strings.Contains(err.Error(), "before the service start") {
		t.Fatalf("pre-service item without remote control: %v", err)
	}
}

// "success" isn't proof: an ignored change is reported.
func TestGoToItemUnconfirmed(t *testing.T) {
	f := newFake(t, true)
	f.ignorePost = true
	c := open(t, f, "secret")
	if _, err := c.AddonAction(context.Background(), "go_to_item", json.RawMessage(`{"index":5}`)); err == nil || !strings.Contains(err.Error(), "didn't go") {
		t.Fatalf("want an unconfirmed error, got %v", err)
	}
}

func TestOffAir(t *testing.T) {
	f := newFake(t, false)
	c := open(t, f, "secret")
	if _, err := c.AddonAction(context.Background(), "go_to_item", json.RawMessage(`{"index":2}`)); err == nil || !strings.Contains(err.Error(), "on air") {
		t.Fatalf("off air: %v", err)
	}
}

func TestGoToSlide(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	ctx := context.Background()
	if _, err := c.AddonAction(ctx, "go_to_slide", json.RawMessage(`{"index":4}`)); err != nil {
		t.Fatal(err)
	}
	if item, slide, _, _ := f.at(); item != 2 || slide != 3 {
		t.Fatalf("at %d/%d", item, slide)
	}
	if _, err := c.AddonAction(ctx, "go_to_slide", json.RawMessage(`{"index":5}`)); err == nil {
		t.Fatal("slide 5 of 4 accepted")
	}
	f.set(func() { f.noControl = true })
	c.ctl = controlAuth{}
	if _, err := c.AddonAction(ctx, "go_to_slide", json.RawMessage(`{"index":2}`)); err != nil {
		t.Fatal(err)
	}
	if _, slide, _, _ := f.at(); slide != 1 {
		t.Fatalf("fallback: slide %d", slide)
	}
	if cmds, _, _, _ := f.counts(); cmds != "GoToSlide:2" {
		t.Fatalf("fallback: cmds %v", cmds)
	}
}

func TestQuickScreensAndVideo(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	ctx := context.Background()
	if res, err := c.AddonAction(ctx, "show_no_text", nil); err != nil || res.(map[string]any)["confirmed"] != true {
		t.Fatalf("no text: %v %v", res, err)
	}
	if res, err := c.AddonAction(ctx, "show_text", nil); err != nil || res.(map[string]any)["confirmed"] != true {
		t.Fatalf("text: %v %v", res, err)
	}
	if _, err := c.AddonAction(ctx, "video_play", nil); err != nil {
		t.Fatal(err)
	}
	if item, slide, _, media := f.at(); media != "Playing" || item != 2 || slide != 1 {
		t.Fatalf("play: %d/%d %s", item, slide, media)
	}
	if _, err := c.AddonAction(ctx, "video_pause", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, media := f.at(); media != "Paused" {
		t.Fatalf("pause: %s", media)
	}
}

func TestShowBible(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	ctx := context.Background()
	if _, err := c.AddonAction(ctx, "show_bible", json.RawMessage(`{"reference":"John 3:16"}`)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	qs, data, item := f.qsKind, f.qsData, f.item
	f.mu.Unlock()
	if qs != "OnScreenBible" || data != "John 3:16" || item != 2 {
		t.Fatalf("bible: %s %q item %d", qs, data, item)
	}
	if _, err := c.AddonAction(ctx, "show_bible", json.RawMessage(`{"reference":" "}`)); err == nil {
		t.Fatal("empty reference accepted")
	}
	st, _ := c.AddonState(ctx)
	if st["quickScreen"] != "OnScreenBible" {
		t.Fatalf("state %v", st)
	}
}

// The status loop long-polls with localRevision / step and ConnectionId,
// and says when Proclaim moves.
func TestStatusLoop(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	ch := make(chan struct{}, 16)
	c.SetNotify(func() { ch <- struct{}{} })
	wait := func(what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("no notice for %s", what)
		}
	}
	wait("the first status")
	f.mu.Lock()
	f.perform("NextSlide", 0)
	f.mu.Unlock()
	wait("next slide")
	// The state comes from the loop's status (no extra status read).
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	before := len(f.polls)
	f.mu.Unlock()
	st, _ := c.AddonState(context.Background())
	if st["slide"] != 3 {
		t.Fatalf("state %v", st)
	}
	f.mu.Lock()
	polls := append([]string(nil), f.polls...)
	f.mu.Unlock()
	if len(polls) != before {
		t.Fatalf("state read the status itself: %v", polls[before:])
	}
	held := false
	for _, p := range polls {
		if strings.Contains(p, "localRevision=7") && strings.Contains(p, "step=") && strings.HasSuffix(p, "|conn-1") {
			held = true
		}
	}
	if !held {
		t.Fatalf("no long poll with ConnectionId: %v", polls)
	}
	f.mu.Lock()
	f.sess = ""
	f.changed.Broadcast()
	f.mu.Unlock()
	wait("off air")
}

func TestCloseLetsGoOfControl(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	if _, err := c.AddonAction(context.Background(), "go_to_slide", json.RawMessage(`{"index":1}`)); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, _, _, n := f.counts(); n != 1 {
		t.Fatalf("disconnects %d", n)
	}
}
