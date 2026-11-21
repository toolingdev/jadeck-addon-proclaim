// Proclaim: a jadeck add-on. Next / previous, go to a service
// item or slide, quick screens, video, and what's on air, over
// Proclaim's Local Remote server.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"

	"github.com/toolingdev/jadeck/sdk/addon"
)

// Version is set by the release build.
var Version = "1.1.0"

func main() { addon.Serve(spec()) }

var (
	sampleSlides = map[string]any{"/api/presentation/status": map[string]any{
		"label": "Proclaim — office PC", "available": true, "port": "Proclaim at 192.168.1.20:52195",
		"actions": []string{"prev", "next", "black", "logo", "clear"},
	}}
	sampleState = sample()
)

// samplePic is a small 16:9 picture for the Settings preview (there is no
// connection there, so samples use data: URLs).
func samplePic(c color.RGBA, bar bool) string {
	img := image.NewRGBA(image.Rect(0, 0, 32, 18))
	for y := 0; y < 18; y++ {
		for x := 0; x < 32; x++ {
			px := c
			if bar && y >= 8 && y <= 9 && x >= 6 && x < 26 {
				px = color.RGBA{230, 230, 230, 255}
			}
			img.Set(x, y, px)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func sample() map[string]any {
	live, next := samplePic(color.RGBA{20, 20, 24, 255}, true), samplePic(color.RGBA{40, 24, 56, 255}, true)
	stage := samplePic(color.RGBA{0, 0, 0, 255}, true)
	strip := []map[string]any{}
	for i := 1; i <= 4; i++ {
		strip = append(strip, map[string]any{"id": i, "label": i, "src": next, "active": i == 2, "next": i == 3})
	}
	row := func(id, title, icon string) map[string]any {
		return map[string]any{"id": id, "title": title, "icon": icon}
	}
	cur := row("c", "John 3:16", "scripture")
	cur["active"], cur["sub"] = true, "slide 1 of 2 · pause after reading"
	st := map[string]any{
		"reachable": true, "onAir": true, "title": "Sunday Service", "currentIndex": 2, "itemTitle": "Amazing Grace",
		"currentId": "c", "slide": 3, "slides": 6, "section": "service",
		"items": []map[string]any{
			{"index": 1, "title": "Welcome"}, {"index": 2, "title": "Amazing Grace"},
			{"index": 3, "title": "Announcements"}, {"index": 4, "title": "Sermon"},
		},
		"liveSrc": live, "liveCaption": "John 3:16 · slide 1 of 2", "liveBadge": "LIVE", "liveTone": "pgm",
		"nextSrc": next, "nextCaption": "Amazing Grace · slide 1 of 6 — tap to take", "nextTone": "pvw", "nextBadge": "",
		"stageSrc": stage, "stageCaption": "What the stage sees: John 3:16", "stageBadge": "",
		"serviceStart": "2026-11-21T18:50:00Z", "clockLabel": "Service started",
		"notes": "<p>Pause after reading. Camera 2 wide on the pulpit. Spanish reading follows.</p>",
		"rundown": []map[string]any{
			{"id": "pre-service", "title": "Pre-service", "meta": "3", "items": []map[string]any{row("a", "Countdown", "video")}},
			{"id": "service", "title": "Service", "meta": "10:50 AM", "items": []map[string]any{
				row("b", "Cuando allá se pase lista", "song"), cur, row("d", "Juan 3:16", "scripture"), row("e", "Offering video", "video")}},
		},
		"strip": strip,
		// The button widgets as their default settings draw them.
		"songParts": songPartButtons(fieldDefault["parts"].([]string)),
		"bibleRecent": []map[string]any{{"value": "John 3:16", "label": "John 3:16"}, {"value": "Psalm 23", "label": "Psalm 23"},
			{"value": "Romans 8:28", "label": "Romans 8:28"}, {"value": "Juan 3:16", "label": "Juan 3:16"}},
		"quickScreens": sampleQuick(),
		"padButtons":   samplePad(),
		"mediaButtons": []map[string]any{
			{"id": "restart", "label": "Restart"}, {"id": "rewind", "label": "−10 s"}, {"id": "play", "label": "Play", "active": true},
			{"id": "pause", "label": "Pause"}, {"id": "forward", "label": "+10 s"}, {"id": "next_audio", "label": "Next audio"},
		},
		"sectionButtons": []map[string]any{
			{"id": "pre-service", "label": "Pre-service"}, {"id": "warm-up", "label": "Warm-up"},
			{"id": "service", "label": "Service", "active": true}, {"id": "post-service", "label": "Post"},
		},
	}
	return st
}

// sampleQuick is the default Quick screens widget, with one quick screen
// of the service ("This service's own").
func sampleQuick() []map[string]any {
	var out []map[string]any
	for _, k := range fieldDefault["screens"].([]string) {
		if l := quickLabel(k); l != "" {
			out = append(out, map[string]any{"id": k, "label": l, "active": k == "Blank"})
		}
	}
	return append(out, map[string]any{"id": "q1", "label": "Prayer Request"})
}

// samplePad is the default Button pad.
func samplePad() []map[string]any {
	var out []map[string]any
	for _, v := range defaultPad {
		if b, ok := padItem(v, nil, nil); ok {
			out = append(out, b)
		}
	}
	return out
}

func spec() addon.Spec {
	return addon.Spec{
		Manifest: addon.Manifest{
			ID: "proclaim", Name: "Proclaim", Version: Version, Publisher: "jadeck", APIVersion: addon.APIVersion, Repo: "toolingdev/jadeck-addon-proclaim",
			Description: "Next / previous, go to a service item, blank, and see what's on air.",
			Reaches:     "The Proclaim PCs you add as connections (Proclaim's network control, port 52195)",
			ConnectionTypes: []addon.ConnectionType{{
				ID: "proclaim", Label: "Proclaim", Capabilities: []string{addon.CapSlides},
				Description: "In Proclaim: Settings → Remote → Local Server → Enable, then pick the PC from the list (or type its address). The password is the Network Control Password there.",
				Fields: []addon.Field{
					{Key: "host", Label: "Proclaim PC address", Type: "text", Default: "127.0.0.1", Placeholder: "192.168.1.20", Required: true, OptionsFrom: optHosts,
						Help: "Proclaim PCs on this network are listed (Local Server on). 127.0.0.1 when Proclaim runs on this same PC (no password needed then). Over Tailscale or a VPN, type the address."},
					{Key: "password", Label: "Network control password", Type: "password",
						Help: "Proclaim: Settings → Remote → Network Control Password. Not needed on the same PC."},
				},
				Sample:     map[string]any{"host": "192.168.1.20", "password": "••••••"},
				SampleTest: "Connected — on air, Sunday Service",
			}},
			Actions: []addon.ActionDef{
				{ID: "go_to_item", Label: "Go to item…", MacroStep: true, Description: "Jump to a service item by its number in the list (1 = first, pre-service items included). Widgets send the item's id instead ({\"id\": ...}).",
					Params: []addon.Field{{Key: "index", Label: "Item number", Type: "number", Required: true, Min: addon.Num(1), Max: addon.Num(254)}}},
				{ID: "go_to_slide", Label: "Go to slide…", MacroStep: true, Description: "Jump to a slide of the current item (1 = first).",
					Params: []addon.Field{{Key: "index", Label: "Slide number", Type: "number", Required: true, Min: addon.Num(1)}}},
				{ID: "take_next", Label: "Take next", MacroStep: true, Description: "Show the slide the Next widget shows."},
				{ID: "next_slide", Label: "Next slide", MacroStep: true},
				{ID: "prev_slide", Label: "Previous slide", MacroStep: true},
				{ID: "next_item", Label: "Next item", MacroStep: true},
				{ID: "prev_item", Label: "Previous item", MacroStep: true},
				{ID: "start_pre_service", Label: "Start pre-service", MacroStep: true},
				{ID: "start_warm_up", Label: "Start warm-up", MacroStep: true},
				{ID: "start_service", Label: "Start service", MacroStep: true},
				{ID: "start_post_service", Label: "Start post-service", MacroStep: true},
				{ID: "show_no_text", Label: "Hide text", MacroStep: true, Description: "Show the slide's background without its text (Proclaim's No Text quick screen)."},
				{ID: "show_text", Label: "Show text", MacroStep: true, Description: "Bring the text back after Hide text."},
				{ID: "video_play", Label: "Play video", MacroStep: true, Description: "Play the video on screen."},
				{ID: "video_pause", Label: "Pause video", MacroStep: true, Description: "Pause the video on screen."},
				{ID: "video_restart", Label: "Restart video", MacroStep: true},
				{ID: "video_rewind", Label: "Video back 10 s", MacroStep: true},
				{ID: "video_forward", Label: "Video ahead 10 s", MacroStep: true},
				{ID: "next_audio", Label: "Next audio", MacroStep: true, Description: "Play the next audio item (Proclaim's background audio)."},
				{ID: "quick_screen", Label: "Quick screen…", MacroStep: true, Description: "Show a quick screen; the one already showing goes away again. Custom: one of this service's own quick screens, by id.",
					Params: []addon.Field{
						{Key: "kind", Label: "Quick screen", Type: "select", Required: true, Default: "Blank", Options: []addon.Option{
							{Value: "Blank", Label: "Blank"}, {Value: "Logo", Label: "Logo"}, {Value: "NoText", Label: "Hide text"}, {Value: "Text", Label: "Show text"},
							{Value: "Amen", Label: "Amen"}, {Value: "Hallelujah", Label: "Hallelujah"}, {Value: "PraiseTheLord", Label: "Praise the Lord"},
							{Value: "FloatingHeart", Label: "Hearts"}, {Value: "FloatingAmen", Label: "Floating amens"},
							{Value: "HeIsRisen", Label: "He is risen"}, {Value: "HeIsRisenWhite", Label: "He is risen (white)"}, {Value: "Custom", Label: "Custom (by id)"}}},
						{Key: "id", Label: "Custom quick screen id", Type: "text", Advanced: true},
					}},
				{ID: "song_part", Label: "Song part…", MacroStep: true, Description: "Go to a part of the song on screen (verse 2, chorus, bridge…). Proclaim's remote doesn't list a song's parts; asking for one it lacks reports an error.",
					Params: []addon.Field{
						{Key: "part", Label: "Part", Type: "select", Required: true, Default: "verse", Options: []addon.Option{
							{Value: "verse", Label: "Verse"}, {Value: "chorus", Label: "Chorus"}, {Value: "pre-chorus", Label: "Pre-chorus"}, {Value: "bridge", Label: "Bridge"},
							{Value: "interlude", Label: "Interlude"}, {Value: "tag", Label: "Tag"}, {Value: "ending", Label: "Ending"}}},
						{Key: "number", Label: "Number", Type: "number", Default: 1, Min: addon.Num(1), Max: addon.Num(254)},
					}},
				{ID: "show_bible", Label: "Show Bible passage…", MacroStep: true, Description: "Show a passage with Proclaim's on-screen Bible (needs Proclaim on air).",
					Params: []addon.Field{{Key: "reference", Label: "Reference", Type: "text", Required: true, Placeholder: "John 3:16"}}},
				{ID: "go_on_air", Label: "Go on air", MacroStep: true},
				{ID: "go_off_air", Label: "Go off air", MacroStep: true},
			},
			Widgets: []addon.WidgetDef{
				{Kind: "core", Type: "slides.prevnext", Label: "Prev / Next", Sample: sampleSlides},
				{Kind: "status", Type: "on_air", Label: "On air", Description: "On air light with the current item and slide.",
					Bind: map[string]any{"on": "onAir", "title": "itemTitle", "slide": "slide", "slides": "slides"}, Sample: sampleState},
				{Kind: "list", Type: "service_items", Label: "Service items", Description: "The service's items; tap one to go there.",
					Bind: map[string]any{"items": "items", "current": "currentIndex", "action": "go_to_item"}, Sample: sampleState},
				{Kind: "core", Type: "slides.button", Label: "Slide button", Sample: sampleSlides, Settings: map[string]any{"action": "black", "label": "Blank"}},

				// v2 shapes (design/WIDGETS-V2.md in jadeck).
				{Kind: addon.KindImage, Type: "live_slide", Label: "Live slide", Description: "The slide on screen now, with the item and slide number.",
					Bind:   map[string]any{"src": "liveSrc", "caption": "liveCaption", "badge": "liveBadge", "tone": "liveTone"},
					Fields: widgetFields["live_slide"], Size: &addon.Size{W: 5, H: 7}, MinPx: &addon.Size{W: 240, H: 170}, Sample: sampleState},
				{Kind: addon.KindImage, Type: "next_slide", Label: "Next", Description: "The slide that comes next; tap it to take it.",
					Bind:   map[string]any{"src": "nextSrc", "caption": "nextCaption", "badge": "nextBadge", "tone": "nextTone", "action": "take_next"},
					Fields: widgetFields["next_slide"], Size: &addon.Size{W: 3, H: 7}, MinPx: &addon.Size{W: 180, H: 140}, Sample: sampleState},
				{Kind: addon.KindImage, Type: "stage_screen", Label: "Stage screen", Description: "What the speaker and band see (Proclaim's confidence screen).",
					Bind:   map[string]any{"src": "stageSrc", "caption": "stageCaption", "badge": "stageBadge"},
					Fields: widgetFields["stage_screen"], Size: &addon.Size{W: 3, H: 7}, MinPx: &addon.Size{W: 180, H: 140}, Sample: sampleState},
				{Kind: addon.KindClock, Type: "service_clock", Label: "Service clock", Description: "The time, and how long since the service started (or until it starts).",
					Bind: map[string]any{"at": "serviceStart", "label": "clockLabel"},
					Size: &addon.Size{W: 2, H: 4}, MinPx: &addon.Size{W: 150, H: 110}, Sample: sampleState},
				{Kind: addon.KindSections, Type: "rundown", Label: "Rundown", Description: "The service by section; tap any item to put it on screen, pre-service too.",
					Bind:   map[string]any{"sections": "rundown", "current": "currentId", "action": "go_to_item", "param": "id"},
					Fields: widgetFields["rundown"], Size: &addon.Size{W: 4, H: 10}, MinPx: &addon.Size{W: 220, H: 200}, Sample: sampleState},
				{Kind: addon.KindStrip, Type: "item_slides", Label: "Slides of this item", Description: "Every slide of the item on screen; tap one to show it.",
					Bind:   map[string]any{"items": "strip", "action": "go_to_slide", "param": "slide"},
					Fields: widgetFields["item_slides"], Size: &addon.Size{W: 5, H: 4}, MinPx: &addon.Size{W: 240, H: 120}, Sample: sampleState},
				{Kind: addon.KindInput, Type: "scripture", Label: "Scripture on screen", Description: "Show a passage with Proclaim's on-screen Bible, in the version chosen there. Recent passages are one tap away.",
					Bind:   map[string]any{"action": "show_bible", "param": "reference", "placeholder": "John 3:16", "choices": "bibleRecent", "button": addon.SettingsRef + "buttonLabel"},
					Fields: widgetFields["scripture"], Size: &addon.Size{W: 3, H: 4}, MinPx: &addon.Size{W: 220, H: 130}, Sample: sampleState},
				{Kind: addon.KindButtons, Type: "song_parts", Label: "Song parts", Description: "Jump to a verse, chorus, bridge or tag while a song is on screen.",
					Bind:   map[string]any{"items": "songParts", "action": "song_part", "columns": 4},
					Fields: widgetFields["song_parts"], Size: &addon.Size{W: 4, H: 5}, MinPx: &addon.Size{W: 240, H: 220}, Sample: sampleState},
				{Kind: addon.KindText, Type: "item_notes", Label: "Notes", Description: "The notes of the item on screen.",
					Bind: map[string]any{"title": "itemTitle", "body": "notes"},
					Size: &addon.Size{W: 5, H: 3}, MinPx: &addon.Size{W: 180, H: 80}, Sample: sampleState},
				{Kind: addon.KindButtons, Type: "quick_screens", Label: "Quick screens", Description: "Blank, logo, hide text, Amen… and this service's own quick screens. Tap the one showing to take it down.",
					Bind:   map[string]any{"items": "quickScreens", "action": "quick_screen"},
					Fields: widgetFields["quick_screens"], Size: &addon.Size{W: 4, H: 5}, MinPx: &addon.Size{W: 200, H: 100}, Sample: sampleState},
				{Kind: addon.KindButtons, Type: "video_audio", Label: "Video & audio", Description: "Restart, skip back or ahead, play, pause, next audio.",
					Bind:   map[string]any{"items": "mediaButtons", "columns": 3},
					Fields: widgetFields["video_audio"], Size: &addon.Size{W: 4, H: 4}, MinPx: &addon.Size{W: 240, H: 160}, Sample: sampleState},
				{Kind: addon.KindButtons, Type: "service_sections", Label: "Service sections", Description: "Start the pre-service, warm-up, service or post-service.",
					Bind:   map[string]any{"items": "sectionButtons", "columns": 4},
					Fields: widgetFields["service_sections"], Size: &addon.Size{W: 4, H: 3}, MinPx: &addon.Size{W: 280, H: 100}, Sample: sampleState},
				{Kind: addon.KindButtons, Type: "button_pad", Label: "Button pad", Description: "Your own set of Proclaim buttons: next, previous, blank, logo, go to item 3, warm-up, show John 3:16… with your labels.",
					Bind:   map[string]any{"items": "padButtons"},
					Fields: widgetFields["button_pad"], Size: &addon.Size{W: 4, H: 3}, MinPx: &addon.Size{W: 140, H: 90}, Sample: sampleState},
			},
			Changelog: "Dashboard widgets: live, next and stage pictures, service clock, rundown, the item's slides, scripture, song parts, notes, quick screens (custom ones too), video & audio, service sections. Go to item lands on the exact item, pre-service included. New: go to slide, warm-up / post-service, hide / show text, play / pause video, on-screen Bible. Live updates from Proclaim. Widget settings: pick and order the quick screens (this service's own too), song parts, video and section buttons; rundown sections, notes and icons; caption, badge and picture quality; thumbnail size; recent passages and the button label. New Button pad widget: any Proclaim actions, your labels.",
		},
		// The widget fields' lists without a connection (a connection's
		// AddonOptions adds the service on air).
		Options: map[string]func() []addon.Option{
			optHosts:        hostOptions,
			optQuickScreens: func() []addon.Option { return quickScreenOptions(nil) },
			optActions:      func() []addon.Option { return padOptions(nil, nil) },
		},
		Open: map[string]addon.OpenFunc{"proclaim": func(raw json.RawMessage) (any, error) { return Open(raw) }},
	}
}
