package main

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/toolingdev/jadeck/sdk/addon"
	"github.com/toolingdev/jadeck/sdk/addon/addontest"
)

// stateFor is AddonStateFor as the controller gets it (plain JSON).
func stateFor(t *testing.T, c *Conn, settings map[string]any) map[string]any {
	t.Helper()
	st, err := c.AddonStateFor(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(st)
	var out map[string]any
	json.Unmarshal(b, &out)
	return out
}

func labels(v any) string {
	var out []string
	for _, b := range list(v) {
		out = append(out, b["label"].(string))
	}
	return strings.Join(out, ",")
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestWidgetFieldsInManifest(t *testing.T) {
	m := spec().Manifest
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, w := range m.Widgets {
		if w.Fields == nil {
			continue
		}
		if _, ok := widgetKeys[w.Type]; !ok {
			t.Errorf("widget %s has fields but no state keys", w.Type)
		}
		for _, f := range w.Fields {
			// AddonStateFor tells widgets apart by their keys.
			if o, dup := seen[f.Key]; dup {
				t.Errorf("field key %s in %s and %s", f.Key, o, w.Type)
			}
			seen[f.Key] = w.Type
			if fieldOwner[f.Key] != w.Type {
				t.Errorf("fieldOwner[%s] = %s", f.Key, fieldOwner[f.Key])
			}
		}
	}
	for w := range widgetKeys {
		if len(widgetFields[w]) == 0 {
			t.Errorf("%s has state keys but no fields", w)
		}
	}
}

// With every widget's defaults, AddonStateFor sends what AddonState
// does for that widget's keys (old dashboards look the same).
func TestDefaultsMatchPlainState(t *testing.T) {
	c := open(t, newFake(t, true), "secret")
	plain := state(t, c)
	for w, fs := range widgetFields {
		set := map[string]any{}
		for _, f := range fs {
			set[f.Key] = f.Default
		}
		got := stateFor(t, c, set)
		want := append(slices.Clone(widgetKeys[w]), "reachable", "onAir")
		slices.Sort(want)
		if !reflect.DeepEqual(keys(got), want) {
			t.Errorf("%s: keys %v, want %v", w, keys(got), want)
		}
		for k, v := range got {
			if !reflect.DeepEqual(v, plain[k]) {
				t.Errorf("%s: %s = %v, plain state has %v", w, k, v, plain[k])
			}
		}
	}
	// Settings naming none of our fields (or {}): the whole state.
	if all := stateFor(t, c, map[string]any{}); len(all) != len(plain) {
		t.Fatalf("{} settings: %d keys, want %d", len(all), len(plain))
	}
}

func TestQuickScreenSettings(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	st := stateFor(t, c, map[string]any{"screens": []any{"custom:prayer request", "Logo", "HeIsRisen", "Blank", "nope", "Logo"}})
	if got := labels(st["quickScreens"]); got != "Prayer Request,Logo,He is risen,Blank" {
		t.Fatalf("picked: %s", got)
	}
	qs := list(st["quickScreens"])
	if p := qs[0]["params"].(map[string]any); p["kind"] != "Custom" || p["id"] != customQS {
		t.Fatalf("custom params %v", p)
	}
	if _, ok := st["mediaButtons"]; ok {
		t.Fatal("quick screens widget got other widgets' keys")
	}
	// "all of this service's own" doesn't repeat one picked by name.
	st = stateFor(t, c, map[string]any{"screens": []any{customAll, "Blank", "custom:Prayer Request"}})
	if got := labels(st["quickScreens"]); got != "Blank,Prayer Request" {
		t.Fatalf("all + named: %s", got)
	}
	// Off air: Proclaim's own, greyed out; the service's are gone.
	f.set(func() { f.sess = "" })
	st = stateFor(t, c, map[string]any{"screens": []any{"custom:Prayer Request", "Blank"}})
	if qs := list(st["quickScreens"]); len(qs) != 1 || qs[0]["disabled"] != true {
		t.Fatalf("off air %v", qs)
	}
}

func TestButtonSettings(t *testing.T) {
	c := open(t, newFake(t, true), "secret") // a song is on
	st := stateFor(t, c, map[string]any{"parts": []any{"chorus1", "verse2", "interlude1", "bogus"}})
	if got := labels(st["songParts"]); got != "C1,V2,Interlude" {
		t.Fatalf("parts %s", got)
	}
	if p := list(st["songParts"])[2]["params"].(map[string]any); p["part"] != "interlude" || p["number"] != 1.0 {
		t.Fatalf("interlude params %v", p)
	}
	// Multi-selects keep the options' order.
	st = stateFor(t, c, map[string]any{"media": []any{"pause", "play"}})
	if got := labels(st["mediaButtons"]); got != "Play,Pause" {
		t.Fatalf("media %s", got)
	}
	st = stateFor(t, c, map[string]any{"sections": []any{"service", "warm-up"}})
	if got := labels(st["sectionButtons"]); got != "Warm-up,Service" || list(st["sectionButtons"])[1]["active"] != true {
		t.Fatalf("sections %v", st["sectionButtons"])
	}
	// Nothing picked: no buttons (the widget says so).
	if st = stateFor(t, c, map[string]any{"media": []any{}}); len(list(st["mediaButtons"])) != 0 {
		t.Fatalf("empty pick %v", st["mediaButtons"])
	}
}

func TestRundownSettings(t *testing.T) {
	c := open(t, newFake(t, true), "secret")
	st := stateFor(t, c, map[string]any{"rundownSections": []any{"service", "pre-service"}, "rundownOpen": []any{"pre-service"},
		"rundownNotes": false, "rundownIcons": false})
	secs := list(st["rundown"])
	if len(secs) != 2 || secs[0]["id"] != "pre-service" || secs[0]["open"] != true || secs[1]["open"] != nil {
		t.Fatalf("sections %v", secs)
	}
	row := list(secs[1]["items"])[0]
	if row["sub"] != "slide 2 of 4" || row["icon"] != "" {
		t.Fatalf("current row %v", row)
	}
	if st["currentId"] != song {
		t.Fatalf("currentId %v", st["currentId"])
	}
}

func TestPictureSettings(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	st := stateFor(t, c, map[string]any{"liveShowCaption": false, "liveShowBadge": false, "liveQuality": "1280"})
	if st["liveSrc"] != "res:slide/"+song+"/1.png?w=1280&rev=1" || st["liveCaption"] != "" || st["liveBadge"] != "" {
		t.Fatalf("live %v", st)
	}
	st = stateFor(t, c, map[string]any{"nextShowBadge": true, "nextQuality": 480.0})
	if st["nextBadge"] != "NEXT" || st["nextCaption"] == "" || !strings.Contains(st["nextSrc"].(string), "w=480") {
		t.Fatalf("next %v", st)
	}
	// A width that isn't offered falls back to 640.
	st = stateFor(t, c, map[string]any{"stageShowBadge": true, "stageQuality": "5000"})
	if st["stageBadge"] != "STAGE" || !strings.Contains(st["stageSrc"].(string), "w=640") {
		t.Fatalf("stage %v", st)
	}
	st = stateFor(t, c, map[string]any{"thumbSize": "large"})
	if s := list(st["strip"])[0]["src"].(string); !strings.Contains(s, "w=960") {
		t.Fatalf("strip %s", s)
	}
	// Off air the caption explains the missing picture even when hidden; no badge.
	f.set(func() { f.sess = "" })
	st = stateFor(t, c, map[string]any{"nextShowCaption": false, "nextShowBadge": true})
	if st["nextCaption"] != "Not on air" || st["nextBadge"] != "" {
		t.Fatalf("off air %v", st)
	}
}

func TestScriptureSettings(t *testing.T) {
	c := open(t, newFake(t, true), "secret")
	refs := []string{"Gen 1:1", "Ex 20:8", "Ps 23", "Is 40:31", "Mt 5:3", "Jn 3:16", "Rom 8:28", "Rev 22:21", "Jn 1:1"}
	for _, r := range refs {
		if _, err := act(t, c, "show_bible", `{"reference":"`+r+`"}`); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(list(stateFor(t, c, map[string]any{"recentCount": 8.0})["bibleRecent"])); n != 8 {
		t.Fatalf("8 asked, %d sent", n)
	}
	if rec := list(stateFor(t, c, map[string]any{"recentCount": "2"})["bibleRecent"]); len(rec) != 2 || rec[0]["value"] != "Jn 1:1" {
		t.Fatalf("2 asked: %v", rec)
	}
	if rec := stateFor(t, c, map[string]any{"recentCount": 0.0})["bibleRecent"]; len(rec.([]any)) != 0 {
		t.Fatalf("0 asked: %v", rec)
	}
	if n := len(list(state(t, c)["bibleRecent"])); n != 4 { // the plain state: 4, as before
		t.Fatalf("plain state %d", n)
	}
}

func TestButtonPad(t *testing.T) {
	f := newFake(t, true) // Opening Song (item 3), slide 2
	c := open(t, f, "secret")
	pad := []any{
		"next_slide", "go_to_item?index=3", "quick_screen?kind=Blank", "quick_screen?kind=Custom&title=Prayer+Request",
		"song_part?number=2&part=verse", "start_service", "go_to_slide?index=2", "show_bible?reference=John+3%3A16",
		"go_on_air", "rm_rf", "go_to_item?index=99", "go_to_item?index=x", "next_slide",
	}
	st := stateFor(t, c, map[string]any{"pad": pad, "padLabels": "next slide = Forward\nShow John 3:16=Jn 3\n = nothing\nbad line"})
	if keys(st)[0] != "onAir" || len(st) != 3 {
		t.Fatalf("keys %v", keys(st))
	}
	btns := list(st["padButtons"])
	if got := labels(st["padButtons"]); got != "Forward,3 · Opening Song,Blank,Prayer Request,Verse 2,Start service,Slide 2,Jn 3,Go on air,Item 99" {
		t.Fatalf("labels %s", got)
	}
	want := []struct {
		active, disabled bool
	}{{false, false}, {true, false}, {false, false}, {false, false}, {false, false}, {true, false}, {true, false}, {false, false}, {false, true}, {false, true}}
	for i, w := range want {
		if btns[i]["active"] != w.active || btns[i]["disabled"] != w.disabled {
			t.Errorf("%s: active %v disabled %v, want %v %v", btns[i]["label"], btns[i]["active"], btns[i]["disabled"], w.active, w.disabled)
		}
	}
	if p := btns[3]["params"].(map[string]any); p["id"] != customQS || p["kind"] != "Custom" {
		t.Fatalf("custom params %v", p)
	}
	// Each button's action and params work as the dashboard sends them.
	for _, i := range []int{1, 2} {
		b, _ := json.Marshal(btns[i]["params"])
		if _, err := act(t, c, btns[i]["action"].(string), string(b)); err != nil {
			t.Fatalf("%s: %v", btns[i]["label"], err)
		}
	}
	if _, _, qs, _ := f.at(); qs != "Blank" {
		t.Fatalf("blank button: quick screen %q", qs)
	}
	// Off air: only Go on air works; a custom quick screen can't be found.
	f.set(func() { f.sess = "" })
	btns = list(stateFor(t, c, map[string]any{"pad": pad})["padButtons"])
	for _, b := range btns {
		if (b["action"] == "go_on_air") == b["disabled"].(bool) {
			t.Errorf("off air: %s disabled %v", b["label"], b["disabled"])
		}
	}
	if btns[1]["label"] != "Item 3" {
		t.Fatalf("off air item label %v", btns[1]["label"])
	}
}

func TestOptions(t *testing.T) {
	c := open(t, newFake(t, true), "secret")
	ctx := context.Background()
	qs, _ := c.AddonOptions(ctx, optQuickScreens)
	if last := qs[len(qs)-1]; last.Value != "custom:Prayer Request" || qs[len(quickKinds)].Value != customAll {
		t.Fatalf("quick screen options %v", qs)
	}
	acts, _ := c.AddonOptions(ctx, optActions)
	vals := map[string]string{}
	for _, o := range acts {
		vals[o.Value] = o.Label
		// Every option makes a button.
		if _, ok := padItem(o.Value, nil, nil); !ok {
			t.Errorf("option %s makes no button", o.Value)
		}
	}
	for v, l := range map[string]string{
		"go_to_item?index=3": "Go to item 3 · Opening Song", "next_slide": "Next slide", "start_warm_up": "Start warm-up",
		"quick_screen?kind=Custom&title=Prayer+Request": "Prayer Request (this service)", "show_bible?reference=John+3%3A16": "Show John 3:16",
		"song_part?number=1&part=chorus": "Song: Chorus 1",
	} {
		if vals[v] != l {
			t.Errorf("%s = %q, want %q", v, vals[v], l)
		}
	}
	if _, ok := vals["go_to_item?index=7"]; ok {
		t.Error("an item the service doesn't have")
	}
	for _, v := range defaultPad {
		if _, ok := vals[v]; !ok {
			t.Errorf("default %s isn't an option", v)
		}
	}
	if o, err := c.AddonOptions(ctx, "other"); o != nil || err != nil {
		t.Fatalf("unknown source: %v %v", o, err)
	}
	// Without a connection: Proclaim's own, and items 1-12.
	if o := spec().Options[optActions](); !slices.ContainsFunc(o, func(x addon.Option) bool { return x.Value == "go_to_item?index=12" }) {
		t.Fatal("offline actions")
	}
}

func TestPadValueRoundTrip(t *testing.T) {
	v := padValue("show_bible", map[string]string{"reference": "Juan 3:16 & 17"})
	q, _ := url.ParseQuery(strings.SplitN(v, "?", 2)[1])
	if q.Get("reference") != "Juan 3:16 & 17" {
		t.Fatalf("%s", v)
	}
	b, ok := padItem(v, nil, nil)
	if !ok || b["label"] != "Juan 3:16 & 17" {
		t.Fatalf("%v", b)
	}
}

func TestSettingsOverThePipe(t *testing.T) {
	f := newFake(t, true)
	u, _ := url.Parse(f.URL)
	h := addontest.Start(t, spec())
	h.Open("p", "proclaim", map[string]any{"host": u.Host, "password": "secret"})
	deadline := time.Now().Add(3 * time.Second)
	var st map[string]any
	for time.Now().Before(deadline) {
		if st = h.State("p", map[string]any{"screens": []any{"Logo", "custom:Prayer Request"}}); len(list(st["quickScreens"])) == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if labels(st["quickScreens"]) != "Logo,Prayer Request" || st["liveSrc"] != nil {
		t.Fatalf("widget state %v", st)
	}
	if plain := h.State("p", nil); plain["liveSrc"] == nil || plain["quickScreens"] == nil {
		t.Fatalf("plain state %v", keys(plain))
	}
	if o := h.Options(optQuickScreens, "p"); o[len(o)-1].Value != "custom:Prayer Request" {
		t.Fatalf("live options %v", o)
	}
	if o := h.Options(optQuickScreens, ""); o[len(o)-1].Value != customAll {
		t.Fatalf("offline options %v", o)
	}
}

// While the status loop runs, widgets asking at once share one snapshot;
// a new status from the loop starts a new one.
func TestStateSnapShared(t *testing.T) {
	f := newFake(t, true)
	c := open(t, f, "secret")
	c.SetNotify(func() {})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		ok := c.watched.ok && c.watched.st != nil
		c.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	a, _ := c.stateSnap(context.Background())
	b, _ := c.stateSnap(context.Background())
	if a.st != b.st || a.p != b.p {
		t.Fatal("not shared")
	}
	f.set(func() { f.slide = 3; f.bump() })
	for time.Now().Before(deadline) {
		if s, _ := c.stateSnap(context.Background()); s.st.Status.SlideIndex == 3 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a new status didn't show")
}
