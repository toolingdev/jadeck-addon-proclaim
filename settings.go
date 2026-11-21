package main

// Per-widget settings (jadeck's WIDGETS-V2.md, v2.1): the fields each
// widget shows in its Inspector, the live option lists behind them
// (addon.Optioner) and AddonStateFor, which builds the state with that
// widget's settings and sends only the keys it shows.
//
// Field keys are unique across all the widgets, so the settings that come
// with a state request say which widget asks.

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/toolingdev/jadeck/sdk/addon"
)

// Options sources (Field.OptionsFrom).
const (
	optQuickScreens = "proclaim.quickscreens"
	optActions      = "proclaim.actions"
)

// customAll stands for every quick screen made in the service on air.
const customAll = "custom:*"

// quickKinds are Proclaim's own quick screens (and Show text), in order.
type quickDef struct{ kind, label string }

var quickKinds = []quickDef{
	{"Blank", "Blank"}, {"Logo", "Logo"}, {"NoText", "Hide text"}, {"Text", "Show text"},
	{"Amen", "Amen"}, {"Hallelujah", "Hallelujah"}, {"PraiseTheLord", "Praise the Lord"},
	{"FloatingHeart", "Hearts"}, {"FloatingAmen", "Floating amens"},
	{"HeIsRisen", "He is risen"}, {"HeIsRisenWhite", "He is risen (white)"},
}

func quickLabel(kind string) string {
	for _, q := range quickKinds {
		if q.kind == kind {
			return q.label
		}
	}
	return ""
}

// allParts are the song parts a Song parts widget can offer; the first
// ten are the default (the 1.x set).
type partDef struct {
	part  string
	n     int
	label string // on the button
	name  string // in the Inspector
}

var allParts = []partDef{
	{"verse", 1, "V1", "Verse 1"}, {"verse", 2, "V2", "Verse 2"}, {"verse", 3, "V3", "Verse 3"}, {"verse", 4, "V4", "Verse 4"},
	{"chorus", 1, "C1", "Chorus 1"}, {"chorus", 2, "C2", "Chorus 2"}, {"pre-chorus", 1, "PC", "Pre-chorus"}, {"bridge", 1, "Bridge", "Bridge"},
	{"tag", 1, "Tag", "Tag"}, {"ending", 1, "End", "Ending"},
	{"verse", 5, "V5", "Verse 5"}, {"verse", 6, "V6", "Verse 6"}, {"chorus", 3, "C3", "Chorus 3"},
	{"pre-chorus", 2, "PC2", "Pre-chorus 2"}, {"bridge", 2, "Bridge 2", "Bridge 2"}, {"interlude", 1, "Interlude", "Interlude"},
}

func partID(part string, n int) string { return fmt.Sprintf("%s%d", part, n) }

// mediaDefs are the Video & audio buttons.
type mediaDef struct{ id, label, action string }

var mediaDefs = []mediaDef{
	{"restart", "Restart", "video_restart"}, {"rewind", "−10 s", "video_rewind"},
	{"play", "Play", "video_play"}, {"pause", "Pause", "video_pause"},
	{"forward", "+10 s", "video_forward"}, {"next_audio", "Next audio", "next_audio"},
}

// Picture widths (px) the image widgets and the strip may ask for.
var (
	widthOptions = []addon.Option{
		{Value: "480", Label: "480 px (loads fastest)"}, {Value: "640", Label: "640 px"},
		{Value: "960", Label: "960 px"}, {Value: "1280", Label: "1280 px (sharpest)"},
	}
	thumbWidths = map[string]int{"small": 240, "medium": thumbWidth, "large": 960}
)

func ids[T any](xs []T, id func(T) string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, id(x))
	}
	return out
}

func opts[T any](xs []T, o func(T) addon.Option) []addon.Option {
	out := make([]addon.Option, 0, len(xs))
	for _, x := range xs {
		out = append(out, o(x))
	}
	return out
}

func imageFields(prefix string, badge bool, badgeText string) []addon.Field {
	return []addon.Field{
		{Key: prefix + "ShowCaption", Label: "Caption", Type: addon.FieldBool, Default: true, Help: "The line under the picture (item and slide)."},
		{Key: prefix + "ShowBadge", Label: "Badge", Type: addon.FieldBool, Default: badge, Help: "The " + badgeText + " badge beside the title."},
		{Key: prefix + "Quality", Label: "Picture quality", Type: addon.FieldSelect, Default: "640", Options: widthOptions,
			Help: "How wide Proclaim draws the slide. Wider is sharper on a big screen; narrower loads faster."},
	}
}

var sectionOptions = opts(sectionNames, func(s sectionDef) addon.Option {
	return addon.Option{Value: s.id, Label: s.title}
})
var sectionIDs = ids(sectionNames, func(s sectionDef) string { return s.id })

// defaultPad is a new Button pad.
var defaultPad = []string{"prev_slide", "next_slide", "quick_screen?kind=Blank", "quick_screen?kind=Logo"}

// widgetFields are each widget's settings (WidgetDef.Fields).
var widgetFields = map[string][]addon.Field{
	"quick_screens": {{Key: "screens", Label: "Buttons", Type: addon.FieldOrder, OptionsFrom: optQuickScreens,
		Default: append(ids(quickButtons, func(q quickDef) string { return q.kind }), customAll),
		Help:    "Which quick screens, in order. “This service's own” adds every quick screen made in the service on air."}},
	"song_parts": {{Key: "parts", Label: "Parts", Type: addon.FieldOrder,
		Options: opts(allParts, func(p partDef) addon.Option { return addon.Option{Value: partID(p.part, p.n), Label: p.name} }),
		Default: ids(allParts[:10], func(p partDef) string { return partID(p.part, p.n) }),
		Help:    "Which parts, in order. Proclaim doesn't say which parts a song has; one it lacks reports an error."}},
	"video_audio": {{Key: "media", Label: "Buttons", Type: addon.FieldSelect, Multiple: true,
		Options: opts(mediaDefs, func(m mediaDef) addon.Option {
			return addon.Option{Value: m.id, Label: m.label}
		}),
		Default: ids(mediaDefs, func(m mediaDef) string { return m.id })}},
	"service_sections": {{Key: "sections", Label: "Sections", Type: addon.FieldSelect, Multiple: true, Options: sectionOptions, Default: sectionIDs}},
	"rundown": {
		{Key: "rundownSections", Label: "Sections", Type: addon.FieldSelect, Multiple: true, Options: sectionOptions, Default: sectionIDs,
			Help: "Which parts of the service the rundown lists."},
		{Key: "rundownOpen", Label: "Start open", Type: addon.FieldSelect, Multiple: true, Options: sectionOptions, Default: []string{},
			Help: "Sections shown unfolded. The one on air always starts open."},
		{Key: "rundownNotes", Label: "Notes line", Type: addon.FieldBool, Default: true, Help: "The item's notes under the item on air."},
		{Key: "rundownIcons", Label: "Item icons", Type: addon.FieldBool, Default: true},
	},
	"live_slide":   imageFields("live", true, "LIVE"),
	"next_slide":   imageFields("next", false, "NEXT"),
	"stage_screen": imageFields("stage", false, "STAGE"),
	"item_slides": {{Key: "thumbSize", Label: "Thumbnail size", Type: addon.FieldSelect, Default: "medium",
		Options: []addon.Option{{Value: "small", Label: "Small (loads fastest)"}, {Value: "medium", Label: "Medium"}, {Value: "large", Label: "Large (sharpest)"}},
		Help:    "How detailed the thumbnails are (240, 320 or 960 px wide). Large looks sharp in a tall strip, even one that fills the screen."}},
	"scripture": {
		{Key: "recentCount", Label: "Recent passages", Type: addon.FieldNumber, Default: 4, Min: addon.Num(0), Max: addon.Num(8),
			Help: "How many recent passages to show as one-tap chips (0 hides them)."},
		{Key: "buttonLabel", Label: "Button label", Type: addon.FieldText, Default: "Show", Placeholder: "Show"},
	},
	"button_pad": {
		{Key: "pad", Label: "Buttons", Type: addon.FieldOrder, OptionsFrom: optActions, Default: defaultPad,
			Help: "Any Proclaim actions, in order: slides, items, quick screens, song parts, sections, video, passages."},
		{Key: "padLabels", Label: "Labels", Type: addon.FieldTextarea, Placeholder: "Go to item 3 = Sermon\nNext slide = ▶",
			Help: "Optional: one per line, the button's name = the label you want."},
	},
}

// fieldOwner maps a field key to its widget; fieldDefault to its default.
var fieldOwner, fieldDefault = func() (map[string]string, map[string]any) {
	own, def := map[string]string{}, map[string]any{}
	for w, fs := range widgetFields {
		for _, f := range fs {
			own[f.Key], def[f.Key] = w, f.Default
		}
	}
	return own, def
}()

// widgetKeys are the state keys each widget with settings reads.
var widgetKeys = map[string][]string{
	"quick_screens": {"quickScreens"}, "song_parts": {"songParts"}, "video_audio": {"mediaButtons"},
	"service_sections": {"sectionButtons"}, "rundown": {"rundown", "currentId"},
	"live_slide":   {"liveSrc", "liveCaption", "liveBadge", "liveTone"},
	"next_slide":   {"nextSrc", "nextCaption", "nextBadge", "nextTone"},
	"stage_screen": {"stageSrc", "stageCaption", "stageBadge"},
	"item_slides":  {"strip"}, "scripture": {"bibleRecent"}, "button_pad": {"padButtons"},
}

// commonKeys go to every widget (connection and error states).
var commonKeys = []string{"reachable", "onAir", "error"}

// view is how the state is drawn: one widget's settings, or the defaults.
type view struct {
	liveW, nextW, stageW, thumbW                                  int
	liveCaption, liveBadge, nextCaption, nextBadge, stageCaption  bool
	stageBadge, rundownNotes, rundownIcons                        bool
	recent                                                        int
	screens, parts, media, sections, rundownSections, rundownOpen []string
	pad                                                           []string
	padLabels                                                     map[string]string
}

// parseView reads settings; missing or unreadable values are the defaults.
func parseView(set map[string]any) view {
	get := func(k string) any {
		if v, ok := set[k]; ok && v != nil {
			return v
		}
		return fieldDefault[k]
	}
	b := func(k string) bool {
		switch v := get(k).(type) {
		case bool:
			return v
		case string:
			if x, err := strconv.ParseBool(v); err == nil {
				return x
			}
		}
		return fieldDefault[k].(bool)
	}
	n := func(k string) (float64, bool) {
		switch v := get(k).(type) {
		case float64:
			return v, true
		case int:
			return float64(v), true
		case string:
			x, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return x, err == nil
		}
		return 0, false
	}
	l := func(k string) []string {
		switch v := get(k).(type) {
		case []string:
			return v
		case []any:
			out := make([]string, 0, len(v))
			for _, x := range v {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
		return fieldDefault[k].([]string)
	}
	width := func(k string) int {
		if w, ok := n(k); ok && slices.ContainsFunc(widthOptions, func(o addon.Option) bool { return o.Value == strconv.Itoa(int(w)) }) {
			return int(w)
		}
		return liveWidth
	}
	v := view{
		liveW: width("liveQuality"), nextW: width("nextQuality"), stageW: width("stageQuality"), thumbW: thumbWidth,
		liveCaption: b("liveShowCaption"), liveBadge: b("liveShowBadge"),
		nextCaption: b("nextShowCaption"), nextBadge: b("nextShowBadge"),
		stageCaption: b("stageShowCaption"), stageBadge: b("stageShowBadge"),
		rundownNotes: b("rundownNotes"), rundownIcons: b("rundownIcons"),
		recent:  4,
		screens: l("screens"), parts: l("parts"), media: l("media"), sections: l("sections"),
		rundownSections: l("rundownSections"), rundownOpen: l("rundownOpen"),
		pad: l("pad"),
	}
	labels, _ := get("padLabels").(string)
	v.padLabels = parseLabels(labels)
	if s, ok := get("thumbSize").(string); ok && thumbWidths[s] > 0 {
		v.thumbW = thumbWidths[s]
	}
	if r, ok := n("recentCount"); ok {
		v.recent = min(max(int(r), 0), maxRecent)
	}
	return v
}

// parseLabels reads "Button name = label" lines (names compared without case).
func parseLabels(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if ok && k != "" && v != "" {
			out[k] = clip(v, 40)
		}
	}
	return out
}

// AddonStateFor is the state for one widget (addon.SettingsStater): drawn
// with its settings and holding only the keys it reads. Settings that
// name no widget of ours get the whole state.
func (c *Conn) AddonStateFor(ctx context.Context, settings map[string]any) (map[string]any, error) {
	w := ""
	for k := range settings {
		if o, ok := fieldOwner[k]; ok {
			w = o
			break
		}
	}
	s, err := c.stateSnap(ctx)
	full := c.buildState(s, err, parseView(settings))
	if w == "" {
		return full, nil
	}
	out := map[string]any{}
	for _, k := range append(slices.Clone(commonKeys), widgetKeys[w]...) {
		if v, ok := full[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// snapTTL is how long one snapshot serves state requests while the status
// loop runs (each widget with settings asks on its own).
const snapTTL = 750 * time.Millisecond

// stateSnap is snap for the widgets' state, shared for a moment between
// widgets while the status loop keeps it fresh (a new status from the loop
// starts a new one).
func (c *Conn) stateSnap(ctx context.Context) (snapshot, error) {
	c.mu.Lock()
	looping, w := c.stop != nil, c.watched.st
	if looping && c.snapFor == w && time.Since(c.snapAt) < snapTTL {
		s, err := c.snapC, c.snapErr
		c.mu.Unlock()
		return s, err
	}
	c.mu.Unlock()
	s, err := c.snap(ctx, true)
	if looping {
		c.mu.Lock()
		c.snapC, c.snapErr, c.snapAt, c.snapFor = s, err, time.Now(), w
		c.mu.Unlock()
	}
	return s, err
}

// ------------------------------------------------------------ options

// AddonOptions answers the widget fields' live lists (addon.Optioner):
// this service's own quick screens, and actions with its item titles.
func (c *Conn) AddonOptions(ctx context.Context, name string) ([]addon.Option, error) {
	if name != optQuickScreens && name != optActions {
		return nil, nil
	}
	s, _ := c.stateSnap(ctx)
	var p *presentation
	if s.sess != "" {
		p = s.p
	}
	if name == optQuickScreens {
		return quickScreenOptions(p), nil
	}
	c.mu.Lock()
	recent := slices.Clone(c.recent)
	c.mu.Unlock()
	return padOptions(p, recent), nil
}

// quickScreenOptions: Proclaim's quick screens, "this service's own", and
// each of them by title (titles carry over to next week's service).
func quickScreenOptions(p *presentation) []addon.Option {
	out := opts(quickKinds, func(q quickDef) addon.Option { return addon.Option{Value: q.kind, Label: q.label} })
	out = append(out, addon.Option{Value: customAll, Label: "This service's own (all of them)"})
	seen := map[string]bool{}
	for _, q := range customQuickScreens(p) {
		if k := strings.ToLower(q.Title); !seen[k] {
			seen[k] = true
			out = append(out, addon.Option{Value: "custom:" + q.Title, Label: q.Title + " (this service)"})
		}
	}
	return out
}

// commonPassages are offered on the Button pad besides the recent ones.
var commonPassages = []string{"John 3:16", "Psalm 23", "Exodus 20:8-11", "Romans 8:28", "Isaiah 40:31", "Matthew 28:19-20"}

// padValue is a Button pad choice: the action, then its params as a query.
func padValue(action string, params map[string]string) string {
	if len(params) == 0 {
		return action
	}
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	return action + "?" + q.Encode()
}

// padOptions lists every action with sensible params; with a service on
// air, its items (with titles) and its own quick screens.
func padOptions(p *presentation, recent []string) []addon.Option {
	var out []addon.Option
	add := func(action string, params map[string]string, label string) {
		out = append(out, addon.Option{Value: padValue(action, params), Label: label})
	}
	for _, a := range []string{"next_slide", "prev_slide", "take_next", "next_item", "prev_item"} {
		add(a, nil, plainLabels[a])
	}
	for _, q := range quickKinds {
		add("quick_screen", map[string]string{"kind": q.kind}, q.label)
	}
	for _, q := range customQuickScreens(p) {
		add("quick_screen", map[string]string{"kind": "Custom", "title": q.Title}, q.Title+" (this service)")
	}
	n := 12
	if p != nil {
		n = min(len(p.ServiceItems), 254)
	}
	for i := 1; i <= n; i++ {
		label := fmt.Sprintf("Go to item %d", i)
		if p != nil {
			label += " · " + clip(p.ServiceItems[i-1].Title, 40)
		}
		add("go_to_item", map[string]string{"index": strconv.Itoa(i)}, label)
	}
	for i := 1; i <= 10; i++ {
		add("go_to_slide", map[string]string{"index": strconv.Itoa(i)}, fmt.Sprintf("Go to slide %d", i))
	}
	for _, s := range sectionNames {
		add(s.action, nil, plainLabels[s.action])
	}
	for _, sp := range allParts {
		add("song_part", map[string]string{"part": sp.part, "number": strconv.Itoa(sp.n)}, "Song: "+sp.name)
	}
	for _, m := range mediaDefs {
		add(m.action, nil, plainLabels[m.action])
	}
	seen := map[string]bool{}
	for _, r := range append(slices.Clone(recent), commonPassages...) {
		if k := strings.ToLower(r); !seen[k] {
			seen[k] = true
			add("show_bible", map[string]string{"reference": r}, "Show "+r)
		}
	}
	add("go_on_air", nil, plainLabels["go_on_air"])
	add("go_off_air", nil, plainLabels["go_off_air"])
	return out
}

// plainLabels are the Button pad's names for actions without params.
var plainLabels = map[string]string{
	"next_slide": "Next slide", "prev_slide": "Previous slide", "take_next": "Take next",
	"next_item": "Next item", "prev_item": "Previous item",
	"start_pre_service": "Start pre-service", "start_warm_up": "Start warm-up",
	"start_service": "Start service", "start_post_service": "Start post-service",
	"video_play": "Play video", "video_pause": "Pause video", "video_restart": "Restart video",
	"video_rewind": "Video −10 s", "video_forward": "Video +10 s", "next_audio": "Next audio",
	"go_on_air": "Go on air", "go_off_air": "Go off air",
}

// padItem turns a Button pad choice into a button, or false when it
// isn't one (an unknown action, missing params).
func padItem(value string, p *presentation, labels map[string]string) (map[string]any, bool) {
	action, query, _ := strings.Cut(value, "?")
	q, err := url.ParseQuery(query)
	if err != nil {
		return nil, false
	}
	params := map[string]any{}
	num := func(k string) (int, bool) {
		n, err := strconv.Atoi(q.Get(k))
		return n, err == nil && n >= 1 && n <= 254
	}
	var name, alt string // the button's name; alt: what the Inspector calls it, if different
	switch action {
	case "go_to_item":
		n, ok := num("index")
		if !ok {
			return nil, false
		}
		params["index"] = n
		name, alt = fmt.Sprintf("Item %d", n), fmt.Sprintf("Go to item %d", n)
		if p != nil && n <= len(p.ServiceItems) {
			name = fmt.Sprintf("%d · %s", n, p.ServiceItems[n-1].Title)
		}
	case "go_to_slide":
		n, ok := num("index")
		if !ok {
			return nil, false
		}
		params["index"] = n
		name, alt = fmt.Sprintf("Slide %d", n), fmt.Sprintf("Go to slide %d", n)
	case "quick_screen":
		kind := q.Get("kind")
		if kind == "Custom" {
			title := q.Get("title")
			if title == "" {
				return nil, false
			}
			name = title
			for _, cq := range customQuickScreens(p) {
				if strings.EqualFold(cq.Title, title) {
					params["id"] = cq.ID
					break
				}
			}
		} else if name = quickLabel(kind); name == "" {
			return nil, false
		}
		params["kind"] = kind
	case "song_part":
		n, ok := num("number")
		part := q.Get("part")
		if _, known := songParts[part]; !ok || !known {
			return nil, false
		}
		params["part"], params["number"] = part, n
		name = strings.ToUpper(part[:1]) + part[1:]
		if n > 1 || part == "verse" || part == "chorus" {
			name += " " + strconv.Itoa(n)
		}
		alt = "Song: " + name
	case "show_bible":
		ref := strings.TrimSpace(q.Get("reference"))
		if ref == "" {
			return nil, false
		}
		params["reference"] = ref
		name, alt = ref, "Show "+ref
	default:
		if name = plainLabels[action]; name == "" {
			return nil, false
		}
	}
	label := name
	for _, k := range []string{name, alt, value} {
		if l, ok := labels[strings.ToLower(k)]; ok && k != "" {
			label = l
			break
		}
	}
	return map[string]any{"id": value, "label": label, "action": action, "params": params}, true
}
