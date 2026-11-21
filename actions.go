package main

// Actions behind the dashboard widgets: go to an item by id, take the
// next slide, quick screens, song parts.

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// goToItemID goes to the service item with this id (the rundown sends ids).
func (c *Conn) goToItemID(ctx context.Context, id string) (map[string]any, error) {
	s, err := c.onAir(ctx)
	if err != nil {
		return nil, err
	}
	for i, it := range s.p.ServiceItems {
		if strings.EqualFold(it.ID, id) {
			return c.goToItem(ctx, i+1)
		}
	}
	return nil, fmt.Errorf("that item isn't in the service on air")
}

// next is the slide after slide n of item i (both 0-based): the next one
// in the item, else the first slide of the next item that has slides.
// ok is false at the end of the service.
func (p *presentation) next(i, n int) (int, int, bool) {
	if i < 0 || i >= len(p.ServiceItems) {
		return 0, 0, false
	}
	if n+1 < len(p.ServiceItems[i].Slides) {
		return i, n + 1, true
	}
	for j := i + 1; j < len(p.ServiceItems); j++ {
		if len(p.ServiceItems[j].Slides) > 0 {
			return j, 0, true
		}
	}
	return 0, 0, false
}

// itemIndex is the position of the item on air, or -1.
func (p *presentation) itemIndex(id string) int {
	for i, it := range p.ServiceItems {
		if it.ID == id {
			return i
		}
	}
	return -1
}

// takeNext shows the slide the Next widget shows.
func (c *Conn) takeNext(ctx context.Context) (map[string]any, error) {
	s, err := c.onAir(ctx)
	if err != nil {
		return nil, err
	}
	i, n, ok := s.p.next(s.p.itemIndex(s.st.Status.ItemID), s.st.Status.SlideIndex)
	if !ok {
		return nil, fmt.Errorf("there's nothing after this slide")
	}
	it := s.p.ServiceItems[i]
	res := map[string]any{"ok": true, "title": it.Title, "slide": n + 1}
	err = c.postStatus(ctx, s.sess, func(cur statusUpdate) statusUpdate {
		cur.Status = onAirStatus{ItemID: it.ID, SlideIndex: n, QuickScreenKind: "None"}
		return cur
	})
	if errors.Is(err, errNoControl) {
		if err := c.Perform(ctx, "NextSlide", 0); err != nil {
			return nil, err
		}
		res["via"] = "app command"
	} else if err != nil {
		return nil, err
	}
	if _, ok := c.waitFor(ctx, s.sess, func(st *statusUpdate) bool {
		return st.Status.ItemID == it.ID && st.Status.SlideIndex == n
	}); !ok {
		return nil, fmt.Errorf("Proclaim didn't go to the next slide")
	}
	return res, nil
}

// builtinQuickScreens are Proclaim's own quick screens: the kind in the
// status → the app command that shows it.
var builtinQuickScreens = map[string]string{
	"Blank": "ShowBlankQuickScreen", "Logo": "ShowLogoQuickScreen", "NoText": "ShowNoTextQuickScreen",
	"Amen": "ShowAmenQuickScreen", "Hallelujah": "ShowHallelujahQuickScreen", "PraiseTheLord": "ShowPraiseTheLordQuickScreen",
	"FloatingHeart": "ShowFloatingHeartsQuickScreen", "FloatingAmen": "ShowFloatingAmensQuickScreen",
	"HeIsRisen": "ShowHeIsRisenQuickScreen", "HeIsRisenWhite": "ShowHeIsRisenWhiteQuickScreen",
}

func showing(st *statusUpdate) string {
	if st == nil || st.Status.QuickScreenKind == "" {
		return "None"
	}
	return st.Status.QuickScreenKind
}

func qsData(st *statusUpdate) string {
	if st == nil || st.Status.QuickScreenData == nil {
		return ""
	}
	return *st.Status.QuickScreenData
}

// quickScreen shows a quick screen: one of Proclaim's (kind), "Text" (the
// slide's text again after Hide text) or a custom one of this service
// (kind "Custom" with its id). Asking for the one already showing takes
// it down again.
func (c *Conn) quickScreen(ctx context.Context, kind, id string) (map[string]any, error) {
	s, err := c.onAir(ctx)
	if err != nil {
		return nil, err
	}
	cur := showing(s.st)
	clear := func(cmd string) (map[string]any, error) {
		if err := c.Perform(ctx, cmd, 0); err != nil {
			return nil, err
		}
		_, ok := c.waitFor(ctx, s.sess, func(st *statusUpdate) bool { return showing(st) == "None" })
		return map[string]any{"ok": true, "cleared": true, "confirmed": ok}, nil
	}
	switch {
	case kind == "Text":
		return clear("ShowText")
	case kind == "Custom":
		return c.customQuickScreen(ctx, s, id)
	}
	cmd, ok := builtinQuickScreens[kind]
	if !ok {
		return nil, fmt.Errorf("Proclaim has no quick screen %q", kind)
	}
	if cur == kind {
		if kind == "NoText" {
			return clear("ShowText")
		}
		return clear("ShowLastSlide")
	}
	if err := c.Perform(ctx, cmd, 0); err != nil {
		return nil, err
	}
	_, shown := c.waitFor(ctx, s.sess, func(st *statusUpdate) bool { return showing(st) == kind })
	return map[string]any{"ok": true, "kind": kind, "confirmed": shown}, nil
}

// customQuickScreen shows (or takes down) one of the service's own quick
// screens: POST statusChanged with kind Custom and its id, else Proclaim's
// ShowCustomQuickScreen command with its 1-based place among them.
func (c *Conn) customQuickScreen(ctx context.Context, s snapshot, id string) (map[string]any, error) {
	n, title := 0, ""
	for _, q := range customQuickScreens(s.p) {
		n++
		if strings.EqualFold(q.ID, id) {
			title = q.Title
			break
		}
	}
	if title == "" {
		return nil, fmt.Errorf("this service has no such quick screen")
	}
	if showing(s.st) == "Custom" && strings.EqualFold(qsData(s.st), id) {
		if err := c.Perform(ctx, "ShowLastSlide", 0); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "cleared": true}, nil
	}
	res := map[string]any{"ok": true, "title": title}
	err := c.postStatus(ctx, s.sess, func(cur statusUpdate) statusUpdate {
		cur.Status = onAirStatus{ItemID: cur.Status.ItemID, SlideIndex: cur.Status.SlideIndex, QuickScreenKind: "Custom", QuickScreenData: &id}
		return cur
	})
	if errors.Is(err, errNoControl) {
		if err := c.Perform(ctx, "ShowCustomQuickScreen", n); err != nil {
			return nil, err
		}
		res["via"] = "app command"
	} else if err != nil {
		return nil, err
	}
	if _, ok := c.waitFor(ctx, s.sess, func(st *statusUpdate) bool { return showing(st) == "Custom" }); !ok {
		return nil, fmt.Errorf("Proclaim didn't show %s", title)
	}
	return res, nil
}

// customQuickScreens are the quick screens made in this service.
func customQuickScreens(p *presentation) []quickScreen {
	var out []quickScreen
	if p == nil {
		return nil
	}
	for _, q := range p.QuickScreens {
		if q.ServiceItemID != nil && *q.ServiceItemID != "" && q.Title != "" {
			out = append(out, q)
		}
	}
	return out
}

// songParts maps the song_part action's part names to Proclaim's.
var songParts = map[string]string{
	"verse": "Verse", "chorus": "Chorus", "prechorus": "PreChorus", "pre-chorus": "PreChorus",
	"bridge": "Bridge", "ending": "Ending", "interlude": "Interlude", "tag": "Tag",
}

// songPart goes to a part of the song on screen (verse 2, chorus 1, ...)
// with Proclaim's ShowSongLyrics<Part>ByIndex command. Proclaim says
// nothing when the song has no such part, so the status is read back.
func (c *Conn) songPart(ctx context.Context, part string, n int) (map[string]any, error) {
	name, ok := songParts[strings.ToLower(strings.TrimSpace(part))]
	if !ok {
		return nil, fmt.Errorf("unknown song part %q (verse, chorus, pre-chorus, bridge, interlude, ending or tag)", part)
	}
	if n < 1 {
		n = 1
	}
	if n > 254 {
		return nil, fmt.Errorf("part number must be 1-254")
	}
	s, err := c.onAir(ctx)
	if err != nil {
		return nil, err
	}
	i := s.p.itemIndex(s.st.Status.ItemID)
	if i < 0 || s.p.ServiceItems[i].Kind != "SongLyrics" {
		return nil, fmt.Errorf("song parts only work while a song is on screen")
	}
	before := s.st.Status
	if err := c.Perform(ctx, "ShowSongLyrics"+name+"ByIndex", n); err != nil {
		return nil, err
	}
	label := fmt.Sprintf("%s %d", strings.ToLower(name), n)
	if _, moved := c.waitFor(ctx, s.sess, func(st *statusUpdate) bool {
		return st.Status.ItemID != before.ItemID || st.Status.SlideIndex != before.SlideIndex || st.Status.Revision != before.Revision
	}); !moved {
		return nil, fmt.Errorf("Proclaim stayed put: %s may have no %s (or it's already showing)", s.p.ServiceItems[i].Title, label)
	}
	return map[string]any{"ok": true, "part": label}, nil
}
