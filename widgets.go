package main

// The add-on's state: what the dashboard widgets bind to (main.go's
// manifest). Pictures are "res:" paths served by Resource (resource.go).

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Widths of the pictures the widgets ask for.
const (
	liveWidth  = 640
	thumbWidth = 320
)

// slideSrc is the res: path of slide n of it (confidence: the stage view).
func slideSrc(it serviceItem, n, w int, confidence bool) string {
	kind := "slide"
	if confidence {
		kind = "confidence"
	}
	return fmt.Sprintf("res:%s/%s/%d.png?w=%d&rev=%d", kind, it.ID, n, w, it.Slides[n].LocalRevision)
}

// ticksEpoch is the Unix epoch in .NET ticks (100 ns since 0001-01-01).
const ticksEpoch = 621355968000000000

// ticksTime turns .NET UTC ticks into a time; zero for no time.
func ticksTime(t int64) time.Time {
	if t <= ticksEpoch {
		return time.Time{}
	}
	return time.Unix(0, (t-ticksEpoch)*100).UTC()
}

// itemIcon is the rundown icon for an item kind.
func itemIcon(kind string) string {
	switch kind {
	case "SongLyrics":
		return "song"
	case "BiblePassage", "OnScreenBible":
		return "scripture"
	case "Video":
		return "video"
	case "ImageSlideshow":
		return "image"
	case "Announcement", "Announcement2":
		return "announcement"
	case "ConfidenceTimerCue":
		return "timer"
	case "StageDirectionCue", "SermonEditorSection":
		return "text"
	case "Grouping":
		return ""
	}
	return "slide"
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// plainNotes is item notes (HTML) as one line of text.
func plainNotes(h string) string {
	h = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</li>`).ReplaceAllString(h, " ")
	t := html.UnescapeString(tagRe.ReplaceAllString(h, ""))
	return strings.Join(strings.Fields(t), " ")
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// sections of the service, in order.
type sectionDef struct{ id, title, action string }

var sectionNames = []sectionDef{
	{"pre-service", "Pre-service", "start_pre_service"},
	{"warm-up", "Warm-up", "start_warm_up"},
	{"service", "Service", "start_service"},
	{"post-service", "Post-service", "start_post_service"},
}

// quick-screen buttons that are always there (Proclaim's own).
var quickButtons = []quickDef{
	{"Blank", "Blank"}, {"Logo", "Logo"}, {"NoText", "Hide text"}, {"Text", "Show text"},
	{"Amen", "Amen"}, {"Hallelujah", "Hallelujah"}, {"FloatingHeart", "Hearts"},
}

// maxRecent is how many Bible references are remembered (the Scripture
// widget shows up to this many).
const maxRecent = 8

// remember notes a Bible reference shown from here (newest first).
func (c *Conn) remember(ref string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []string{ref}
	for _, r := range c.recent {
		if !strings.EqualFold(r, ref) && len(out) < maxRecent {
			out = append(out, r)
		}
	}
	c.recent = out
}

// AddonState is everything the widgets show, drawn with the widgets'
// default settings. Keys from 1.x (reachable, onAir, title, items,
// currentIndex, itemTitle, slide, slides, section, quickScreen,
// mediaState, error) are kept as they were.
func (c *Conn) AddonState(ctx context.Context) (map[string]any, error) {
	s, err := c.stateSnap(ctx)
	return c.buildState(s, err, parseView(nil)), nil
}

// buildState is the state from a snapshot (and the error reading it),
// drawn the way v says (one widget's settings, or the defaults).
func (c *Conn) buildState(s snapshot, err error, v view) map[string]any {
	out := map[string]any{
		"reachable": false, "onAir": false, "items": []any{}, "currentIndex": 0,
		// widget keys, filled in below when on air
		"itemTitle": "", "currentId": "", "notes": "",
		"liveSrc": "", "liveCaption": "Not on air", "liveBadge": "", "liveTone": "none",
		"nextSrc": "", "nextCaption": "Not on air", "nextTone": "none", "nextBadge": "",
		"stageSrc": "", "stageCaption": "Not on air", "stageBadge": "",
		"serviceStart": "", "clockLabel": "Not on air",
		"rundown": []any{}, "strip": []any{}, "songParts": []any{},
	}
	c.mu.Lock()
	recent := make([]map[string]any, 0, len(c.recent))
	for _, r := range c.recent[:min(len(c.recent), v.recent)] {
		recent = append(recent, map[string]any{"value": r, "label": r})
	}
	c.mu.Unlock()
	out["bibleRecent"] = recent

	on := s.sess != "" && s.p != nil && s.st != nil
	fill(out, s, err, on, v)
	controls(out, s, on, v)
	finish(out, v)
	return out
}

// fill puts in what's on air: pictures, captions, rundown, strip, parts.
func fill(out map[string]any, s snapshot, err error, on bool, v view) {
	if s.sess == "" && err != nil {
		out["error"] = err.Error()
		out["liveCaption"], out["nextCaption"], out["stageCaption"], out["clockLabel"] = "Proclaim isn't answering", "", "", ""
		return
	}
	out["reachable"] = true
	if s.sess == "" {
		return
	}
	out["onAir"] = true
	if err != nil {
		out["error"] = err.Error()
	}
	if !on {
		return
	}
	p, st := s.p, s.st
	out["title"] = p.Title
	if t := ticksTime(p.StartTime); !t.IsZero() {
		out["serviceStart"] = t.Format(time.RFC3339)
		if time.Now().Before(t) {
			out["clockLabel"] = "Service starts"
		} else {
			out["clockLabel"] = "Service started"
		}
	} else {
		out["clockLabel"] = ""
	}

	cur := p.itemIndex(st.Status.ItemID)
	items := make([]map[string]any, 0, len(p.ServiceItems))
	for i, it := range p.ServiceItems {
		items = append(items, map[string]any{"index": i + 1, "title": it.Title, "kind": it.Kind, "slides": len(it.Slides), "section": p.section(i)})
	}
	out["items"] = items
	qs := showing(st)
	if qs != "None" {
		out["quickScreen"] = qs
	}
	if m := st.media(); m != "" {
		out["mediaState"] = m
	}

	// Live, stage and the current item.
	if cur >= 0 {
		it := p.ServiceItems[cur]
		n := st.Status.SlideIndex
		out["currentIndex"] = cur + 1
		out["currentId"] = it.ID
		out["itemTitle"] = it.Title
		out["slide"] = n + 1
		out["slides"] = len(it.Slides)
		out["section"] = p.section(cur)
		out["notes"] = it.Notes
		where := fmt.Sprintf("%s · slide %d of %d", it.Title, n+1, len(it.Slides))
		if n >= 0 && n < len(it.Slides) {
			out["liveSrc"] = slideSrc(it, n, v.liveW, false)
			out["stageSrc"] = slideSrc(it, n, v.stageW, true)
		} else {
			where = it.Title
		}
		if qs == "Custom" {
			for _, q := range customQuickScreens(p) {
				if strings.EqualFold(q.ID, qsData(st)) {
					where += " · " + q.Title
				}
			}
		} else if qs != "None" {
			where += " · " + quickName(qs)
		}
		out["liveCaption"], out["liveBadge"], out["liveTone"] = where, "LIVE", "pgm"
		out["stageCaption"] = "What the stage sees: " + it.Title

		strip := make([]map[string]any, 0, len(it.Slides))
		for k := range it.Slides {
			if k >= 200 {
				break
			}
			strip = append(strip, map[string]any{"id": fmt.Sprint(k + 1), "label": fmt.Sprint(k + 1),
				"src": slideSrc(it, k, v.thumbW, false), "active": k == n, "next": k == n+1})
		}
		out["strip"] = strip

		if it.Kind == "SongLyrics" {
			out["songParts"] = songPartButtons(v.parts)
		}
	} else {
		out["liveCaption"], out["liveBadge"], out["liveTone"] = "On air", "LIVE", "pgm"
		out["stageCaption"] = ""
	}

	// Next.
	if ni, nn, ok := p.next(cur, st.Status.SlideIndex); ok && cur >= 0 {
		it := p.ServiceItems[ni]
		out["nextSrc"] = slideSrc(it, nn, v.nextW, false)
		out["nextCaption"] = fmt.Sprintf("%s · slide %d of %d — tap to take", it.Title, nn+1, len(it.Slides))
		out["nextTone"] = "pvw"
	} else {
		out["nextCaption"] = "End of the service"
	}

	// Rundown: items folded into the service's sections.
	rundown := []map[string]any{}
	for _, sec := range sectionNames {
		if !slices.Contains(v.rundownSections, sec.id) {
			continue
		}
		var rows []map[string]any
		for i, it := range p.ServiceItems {
			if p.section(i) != sec.id {
				continue
			}
			row := map[string]any{"id": it.ID, "title": it.Title, "icon": ""}
			if v.rundownIcons {
				row["icon"] = itemIcon(it.Kind)
			}
			if i == cur {
				row["active"] = true
				sub := fmt.Sprintf("slide %d of %d", st.Status.SlideIndex+1, len(it.Slides))
				if n := plainNotes(it.Notes); n != "" && v.rundownNotes {
					sub += " · " + clip(n, 80)
				}
				row["sub"] = sub
			} else if len(it.Slides) == 0 {
				row["sub"] = "no slides"
			}
			rows = append(rows, row)
		}
		if len(rows) == 0 {
			continue
		}
		meta := fmt.Sprint(len(rows))
		if sec.id == "service" {
			if t := ticksTime(p.StartTime); !t.IsZero() {
				meta = t.Local().Format("3:04 PM")
			}
		}
		section := map[string]any{"id": sec.id, "title": sec.title, "meta": meta, "items": rows}
		if slices.Contains(v.rundownOpen, sec.id) {
			section["open"] = true
		}
		rundown = append(rundown, section)
	}
	out["rundown"] = rundown
}

// finish applies the picture widgets' caption and badge settings. A
// caption that explains a missing picture (not on air...) stays.
func finish(out map[string]any, v view) {
	for _, p := range []struct {
		name           string
		caption, badge bool
		badgeText      string
	}{{"live", v.liveCaption, v.liveBadge, ""}, {"next", v.nextCaption, v.nextBadge, "NEXT"}, {"stage", v.stageCaption, v.stageBadge, "STAGE"}} {
		has := out[p.name+"Src"] != ""
		if !p.caption && has {
			out[p.name+"Caption"] = ""
		}
		switch {
		case !p.badge || (p.badgeText != "" && !has):
			out[p.name+"Badge"] = ""
		case p.badgeText != "":
			out[p.name+"Badge"] = p.badgeText
		} // live: "LIVE" from fill while on air
	}
}

// songPartButtons are the Song parts buttons, in the order picked.
func songPartButtons(picked []string) []map[string]any {
	out := make([]map[string]any, 0, len(picked))
	for _, id := range picked {
		for _, p := range allParts {
			if partID(p.part, p.n) == id {
				out = append(out, map[string]any{"id": id, "label": p.label, "params": map[string]any{"part": p.part, "number": p.n}})
				break
			}
		}
	}
	return out
}

// controls fills in the button widgets' state: quick screens, video,
// service sections, the Button pad, each as v picks. Off air they show,
// greyed out.
func controls(out map[string]any, s snapshot, on bool, v view) {
	qs, media, data := "None", "", ""
	var p *presentation
	cur, curItem, slide := "", -1, -1
	if on {
		qs, media, data, p = showing(s.st), s.st.media(), qsData(s.st), s.p
		if curItem = p.itemIndex(s.st.Status.ItemID); curItem >= 0 {
			cur, slide = p.section(curItem), s.st.Status.SlideIndex
		}
	}
	hasSection := func(id string) bool {
		if p != nil {
			for i := range p.ServiceItems {
				if p.section(i) == id {
					return true
				}
			}
		}
		return false
	}

	// Quick screens: picked kinds, then custom ones by title or all of them.
	quick := []map[string]any{}
	listed := map[string]bool{}
	addCustom := func(q quickScreen) {
		if listed[q.ID] {
			return
		}
		listed[q.ID] = true
		quick = append(quick, map[string]any{"id": q.ID, "label": q.Title, "params": map[string]any{"kind": "Custom", "id": q.ID},
			"active": qs == "Custom" && strings.EqualFold(data, q.ID)})
	}
	for _, pick := range v.screens {
		title, custom := strings.CutPrefix(pick, "custom:")
		switch {
		case !custom:
			if l := quickLabel(pick); l != "" && !listed[pick] {
				listed[pick] = true
				quick = append(quick, map[string]any{"id": pick, "label": l, "params": map[string]any{"kind": pick},
					"active": qs == pick, "disabled": !on})
			}
		case title == "*":
			for _, q := range customQuickScreens(p) {
				// ones picked by name later keep their place
				if !slices.ContainsFunc(v.screens, func(x string) bool { return strings.EqualFold(x, "custom:"+q.Title) }) {
					addCustom(q)
				}
			}
		default:
			for _, q := range customQuickScreens(p) {
				if strings.EqualFold(q.Title, title) {
					addCustom(q)
				}
			}
		}
	}
	out["quickScreens"] = quick

	media2 := []map[string]any{}
	for _, m := range mediaDefs {
		if slices.Contains(v.media, m.id) {
			media2 = append(media2, map[string]any{"id": m.id, "label": m.label, "action": m.action,
				"active": (m.id == "play" && media == "Playing") || (m.id == "pause" && media == "Paused"), "disabled": !on})
		}
	}
	out["mediaButtons"] = media2

	secs := []map[string]any{}
	for _, sec := range sectionNames {
		if !slices.Contains(v.sections, sec.id) {
			continue
		}
		label := sec.title
		if sec.id == "post-service" {
			label = "Post"
		}
		secs = append(secs, map[string]any{"id": sec.id, "label": label, "action": sec.action, "active": cur == sec.id, "disabled": !hasSection(sec.id)})
	}
	out["sectionButtons"] = secs

	// Button pad: any actions, greyed out when they can't work now.
	reachable, _ := out["reachable"].(bool)
	pad := []map[string]any{}
	seen := map[string]bool{}
	for _, val := range v.pad {
		b, ok := padItem(val, p, v.padLabels)
		if !ok || seen[val] {
			continue
		}
		seen[val] = true
		action, params := b["action"].(string), b["params"].(map[string]any)
		active, can := false, on
		switch action {
		case "quick_screen":
			kind, _ := params["kind"].(string)
			id, _ := params["id"].(string)
			active = (kind != "Custom" && qs == kind) || (kind == "Custom" && id != "" && qs == "Custom" && strings.EqualFold(data, id))
			can = on && (kind != "Custom" || id != "")
		case "go_to_item":
			n := params["index"].(int)
			active, can = on && curItem == n-1, on && n <= len(p.ServiceItems)
		case "go_to_slide":
			n := params["index"].(int)
			active, can = on && slide == n-1, on && curItem >= 0 && n <= len(p.ServiceItems[curItem].Slides)
		case "song_part":
			can = on && curItem >= 0 && p.ServiceItems[curItem].Kind == "SongLyrics"
		case "video_play":
			active = media == "Playing"
		case "video_pause":
			active = media == "Paused"
		case "go_on_air":
			can = reachable && !on
		case "go_off_air":
			can = on
		}
		for _, sec := range sectionNames {
			if sec.action == action {
				active, can = cur == sec.id, hasSection(sec.id)
			}
		}
		b["active"], b["disabled"] = active, !can
		pad = append(pad, b)
	}
	out["padButtons"] = pad
}

// quickName is a quick screen kind as people say it.
func quickName(kind string) string {
	switch kind {
	case "NoText":
		return "text hidden"
	case "OnScreenBible":
		return "on-screen Bible"
	case "FloatingHeart":
		return "hearts"
	case "FloatingAmen":
		return "amens"
	case "Custom":
		return "custom quick screen"
	}
	return strings.ToLower(kind)
}
