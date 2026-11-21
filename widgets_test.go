package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/toolingdev/jadeck/sdk/addon"
	"github.com/toolingdev/jadeck/sdk/addon/addontest"
)

func act(t *testing.T, c *Conn, name, params string) (map[string]any, error) {
	t.Helper()
	res, err := c.AddonAction(context.Background(), name, json.RawMessage(params))
	m, _ := res.(map[string]any)
	return m, err
}

func state(t *testing.T, c *Conn) map[string]any {
	t.Helper()
	st, err := c.AddonState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// What the controller gets: plain JSON.
	b, _ := json.Marshal(st)
	var out map[string]any
	json.Unmarshal(b, &out)
	return out
}

func list(v any) []map[string]any {
	var out []map[string]any
	for _, x := range v.([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

const song = "10000000-0000-0000-0000-000000000003"

func TestWidgetState(t *testing.T) {
	f := newFake(t, true) // at Opening Song (item 3), slide 2 of 4
	c := open(t, f, "secret")
	st := state(t, c)
	want := map[string]any{
		"currentId": song, "itemTitle": "Opening Song", "notes": "<p>Key of <b>G</b>&amp;slow</p>",
		"liveSrc":     "res:slide/" + song + "/1.png?w=640&rev=1",
		"stageSrc":    "res:confidence/" + song + "/1.png?w=640&rev=1",
		"nextSrc":     "res:slide/" + song + "/2.png?w=640&rev=1",
		"liveCaption": "Opening Song · slide 2 of 4", "liveBadge": "LIVE", "liveTone": "pgm", "nextTone": "pvw",
		"clockLabel": "Service started",
	}
	for k, v := range want {
		if st[k] != v {
			t.Errorf("%s = %v, want %v", k, st[k], v)
		}
	}
	// 638000000000000000 ticks = 2022-09-28 22:13:20 UTC.
	if st["serviceStart"] != "2022-09-28T22:13:20Z" {
		t.Errorf("serviceStart %v", st["serviceStart"])
	}
	secs := list(st["rundown"])
	var titles []string
	for _, s := range secs {
		titles = append(titles, s["title"].(string))
	}
	if strings.Join(titles, ",") != "Pre-service,Warm-up,Service,Post-service" {
		t.Fatalf("sections %v", titles)
	}
	rows := list(secs[2]["items"])
	if len(rows) != 3 || rows[0]["id"] != song || rows[0]["active"] != true || rows[0]["icon"] != "song" ||
		rows[0]["sub"] != "slide 2 of 4 · Key of G&slow" || rows[1]["sub"] != "no slides" || rows[1]["icon"] != "timer" {
		t.Fatalf("service rows %v", rows)
	}
	strip := list(st["strip"])
	if len(strip) != 4 || strip[1]["active"] != true || strip[2]["next"] != true || strip[0]["id"] != "1" ||
		strip[3]["src"] != "res:slide/"+song+"/3.png?w=320&rev=1" {
		t.Fatalf("strip %v", strip)
	}
	if parts := list(st["songParts"]); len(parts) < 8 || parts[0]["label"] != "V1" {
		t.Fatalf("song parts %v", parts)
	}
	qs := list(st["quickScreens"])
	last := qs[len(qs)-1]
	if last["label"] != "Prayer Request" || last["id"] != customQS || len(qs) != len(quickButtons)+1 {
		t.Fatalf("quick screens %v", qs)
	}
	secBtns := list(st["sectionButtons"])
	if secBtns[2]["active"] != true || secBtns[0]["active"] != false || secBtns[0]["disabled"] != false {
		t.Fatalf("section buttons %v", secBtns)
	}

	// Last slide of the song: the next one skips the item without slides.
	f.set(func() { f.slide = 3 })
	st = state(t, c)
	if st["nextSrc"] != "res:slide/10000000-0000-0000-0000-000000000005/0.png?w=640&rev=1" {
		t.Fatalf("next across items: %v", st["nextSrc"])
	}
	if p := list(st["songParts"]); len(p) == 0 {
		t.Fatal("song parts gone")
	}
	// The end of the service; not a song.
	f.set(func() { f.item, f.slide, f.qsKind = 5, 0, "Blank" })
	st = state(t, c)
	if st["nextSrc"] != "" || st["nextCaption"] != "End of the service" || len(list(st["songParts"])) != 0 ||
		st["liveCaption"] != "Thank You · slide 1 of 1 · blank" || list(st["quickScreens"])[0]["active"] != true {
		t.Fatalf("end of service: %v", st)
	}
}

func TestWidgetStateOffAir(t *testing.T) {
	c := open(t, newFake(t, false), "secret")
	st := state(t, c)
	if st["liveCaption"] != "Not on air" || st["liveSrc"] != "" || st["itemTitle"] != "" || len(list(st["rundown"])) != 0 {
		t.Fatalf("off air %v", st)
	}
	for _, b := range list(st["quickScreens"]) {
		if b["disabled"] != true {
			t.Fatalf("quick screen usable off air: %v", b)
		}
	}
	for _, b := range list(st["sectionButtons"]) {
		if b["disabled"] != true {
			t.Fatalf("section usable off air: %v", b)
		}
	}
}

func TestResources(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	ctx := context.Background()
	ct, b, err := c.Resource(ctx, "slide/"+song+"/1.png?w=9999&rev=1")
	if err != nil || ct != "image/png" || !bytes.Equal(b, tinyPNG) {
		t.Fatalf("slide: %q %v", ct, err)
	}
	if _, _, err := c.Resource(ctx, "slide/"+song+"/1.png?w=9999&rev=1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Resource(ctx, "confidence/"+song+"/0.png?w=320&rev=1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Resource(ctx, "thumbnail.png?w=200"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	imgs := append([]string(nil), f.images...)
	f.mu.Unlock()
	// 202 then 200 for each picture; the repeat came from the cache.
	if len(imgs) != 6 {
		t.Fatalf("image requests %v", imgs)
	}
	q, _ := url.ParseQuery(strings.SplitN(imgs[0], "?", 2)[1])
	if q.Get("width") != "1280" || q.Get("localRevision") != "1" || q.Get("longPolling") != "true" || q.Has("confidence") {
		t.Fatalf("slide query %v", q)
	}
	if !strings.Contains(imgs[2], "/slides/0/image") || !strings.Contains(imgs[2], "confidence=true") {
		t.Fatalf("confidence request %v", imgs[2])
	}
	for _, bad := range []string{"slide/" + song + "/9.png", "slide/not-a-guid/1.png", "../etc/passwd", "slide/" + song + "/x.png", "other.png"} {
		if _, _, err := c.Resource(ctx, bad); !errors.Is(err, addon.ErrNotFound) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if _, _, err := c.Resource(ctx, "slide/"+song+"/1.png?w=abc"); err == nil {
		t.Error("bad width accepted")
	}
}

func TestImageCacheBounded(t *testing.T) {
	var ic imageCache
	for i := range cacheEntries + 10 {
		ic.put(string(rune('a'+i%26))+strings.Repeat("x", i), []byte{1})
	}
	if ic.len() != cacheEntries {
		t.Fatalf("cache holds %d", ic.len())
	}
	ic.put("big", make([]byte, cacheBytes+1))
	if ic.len() != 1 {
		t.Fatalf("a huge picture should push the rest out: %d", ic.len())
	}
}

func TestWidgetActions(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	// Rundown: by id, pre-service too.
	if _, err := act(t, c, "go_to_item", `{"id":"10000000-0000-0000-0000-000000000001"}`); err != nil {
		t.Fatal(err)
	}
	if item, _, _, _ := f.at(); item != 0 {
		t.Fatalf("go_to_item by id: at %d", item)
	}
	if _, err := act(t, c, "go_to_item", `{"id":"10000000-0000-0000-0000-0000000000ff"}`); err == nil {
		t.Fatal("unknown id accepted")
	}
	// Strip: slide as text, 1-based.
	act(t, c, "go_to_item", `{"index":3}`)
	if _, err := act(t, c, "go_to_slide", `{"slide":"3"}`); err != nil {
		t.Fatal(err)
	}
	if _, slide, _, _ := f.at(); slide != 2 {
		t.Fatalf("strip slide: %d", slide)
	}
	// Take next twice: slide 4, then over the empty item to Reading.
	for _, want := range [][2]int{{2, 3}, {4, 0}} {
		if _, err := act(t, c, "take_next", `{}`); err != nil {
			t.Fatal(err)
		}
		if item, slide, _, _ := f.at(); item != want[0] || slide != want[1] {
			t.Fatalf("take next: at %d/%d, want %v", item, slide, want)
		}
	}
	// Quick screens toggle.
	if r, err := act(t, c, "quick_screen", `{"kind":"Amen"}`); err != nil || r["confirmed"] != true {
		t.Fatalf("amen: %v %v", r, err)
	}
	if r, err := act(t, c, "quick_screen", `{"kind":"Amen"}`); err != nil || r["cleared"] != true {
		t.Fatalf("amen again: %v %v", r, err)
	}
	if _, err := act(t, c, "quick_screen", `{"kind":"Custom","id":"`+customQS+`"}`); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	qs, data := f.qsKind, f.qsData
	f.mu.Unlock()
	if qs != "Custom" || data != customQS {
		t.Fatalf("custom quick screen: %s %s", qs, data)
	}
	if st := state(t, c); list(st["quickScreens"])[len(quickButtons)]["active"] != true {
		t.Fatalf("custom not active: %v", st["quickScreens"])
	}
	if r, err := act(t, c, "quick_screen", `{"kind":"Custom","id":"`+customQS+`"}`); err != nil || r["cleared"] != true {
		t.Fatalf("custom again: %v %v", r, err)
	}
	if _, err := act(t, c, "quick_screen", `{"kind":"Custom","id":"nope"}`); err == nil {
		t.Fatal("unknown custom quick screen accepted")
	}
	if _, err := act(t, c, "quick_screen", `{"kind":"Dance"}`); err == nil {
		t.Fatal("unknown quick screen accepted")
	}
	// Song parts: only on a song; a missing part is an error.
	if _, err := act(t, c, "song_part", `{"part":"verse","number":2}`); err == nil || !strings.Contains(err.Error(), "while a song") {
		t.Fatalf("song part off a song: %v", err)
	}
	act(t, c, "go_to_item", `{"id":"`+song+`"}`)
	if _, err := act(t, c, "song_part", `{"part":"chorus","number":1}`); err != nil {
		t.Fatal(err)
	}
	if _, slide, _, _ := f.at(); slide != 2 {
		t.Fatalf("chorus: slide %d", slide)
	}
	if _, err := act(t, c, "song_part", `{"part":"bridge","number":1}`); err == nil || !strings.Contains(err.Error(), "may have no bridge 1") {
		t.Fatalf("missing part: %v", err)
	}
	if _, err := act(t, c, "song_part", `{"part":"solo"}`); err == nil {
		t.Fatal("unknown part accepted")
	}
	// Video and audio commands.
	for _, a := range []string{"video_restart", "video_rewind", "video_forward", "next_audio"} {
		if _, err := act(t, c, a, `{}`); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
	}
	cmds, _, _, _ := f.counts()
	for _, want := range []string{"ShowAmenQuickScreen:", "ShowLastSlide:", "ShowSongLyricsChorusByIndex:1", "ShowSongLyricsBridgeByIndex:1", "VideoFastForward:", "NextAudioItem:"} {
		if !strings.Contains(cmds, want) {
			t.Errorf("no %s in %s", want, cmds)
		}
	}
}

// Without the remote-control login, custom quick screens use
// ShowCustomQuickScreen with their place among the custom ones.
func TestCustomQuickScreenFallback(t *testing.T) {
	f := newFake(t, true)
	f.noControl = true
	c := open(t, f, "secret")
	if r, err := act(t, c, "quick_screen", `{"kind":"Custom","id":"`+customQS+`"}`); err != nil || r["via"] != "app command" {
		t.Fatalf("%v %v", r, err)
	}
	if cmds, _, _, _ := f.counts(); cmds != "ShowCustomQuickScreen:1" {
		t.Fatalf("sent %s", cmds)
	}
}

func TestBibleRecent(t *testing.T) {
	c := open(t, newFake(t, true), "secret")
	for _, r := range []string{"John 3:16", "Psalm 23", "john 3:16"} {
		if _, err := act(t, c, "show_bible", `{"reference":"`+r+`"}`); err != nil {
			t.Fatal(err)
		}
	}
	rec := list(state(t, c)["bibleRecent"])
	if len(rec) != 2 || rec[0]["value"] != "john 3:16" || rec[1]["value"] != "Psalm 23" {
		t.Fatalf("recent %v", rec)
	}
}

func TestHostPort(t *testing.T) {
	c, err := Open(json.RawMessage(`{"host":"http://10.0.0.5:52196/"}`))
	if err != nil || c.base != "http://10.0.0.5:52196" || c.local {
		t.Fatalf("%v %v", c, err)
	}
	c, _ = Open(json.RawMessage(`{"host":"127.0.0.1"}`))
	if c.base != "http://127.0.0.1:52195" || !c.local {
		t.Fatalf("%v", c.base)
	}
}

// Over the add-on pipe: the state and a picture, as the controller asks.
func TestResourceOverThePipe(t *testing.T) {
	f := newFake(t, true)
	u, _ := url.Parse(f.URL)
	h := addontest.Start(t, spec())
	h.Open("p", "proclaim", map[string]any{"host": u.Host, "password": "secret"})
	deadline := time.Now().Add(3 * time.Second)
	var st map[string]any
	for time.Now().Before(deadline) {
		h.Call("p", "addon.state", nil, &st)
		if st["liveSrc"] != "" && st["liveSrc"] != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	src, _ := st["liveSrc"].(string)
	if !strings.HasPrefix(src, "res:") {
		t.Fatalf("liveSrc %v", st["liveSrc"])
	}
	var res addon.ResourceResult
	h.Call("p", "addon.resource", map[string]any{"path": strings.TrimPrefix(src, "res:")}, &res)
	if res.ContentType != "image/png" || !bytes.Equal(res.Data, tinyPNG) {
		t.Fatalf("resource %q %d bytes", res.ContentType, len(res.Data))
	}
}
