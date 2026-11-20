// Proclaim: a jadeck add-on. Next / previous, go to a service
// item, blank, and what's on air, over Proclaim's network control.
package main

import (
	"encoding/json"

	"github.com/toolingdev/jadeck/sdk/addon"
)

// Version is set by the release build.
var Version = "1.0.0"

func main() { addon.Serve(spec()) }

var (
	sampleSlides = map[string]any{"/api/presentation/status": map[string]any{
		"label": "Proclaim — office PC", "available": true, "port": "Proclaim at 192.168.1.20:52195",
		"actions": []string{"prev", "next", "black", "logo", "clear"},
	}}
	sampleState = map[string]any{
		"reachable": true, "onAir": true, "title": "Sabbath Service", "currentIndex": 2, "itemTitle": "Amazing Grace",
		"slide": 3, "slides": 6,
		"items": []map[string]any{
			{"index": 1, "title": "Welcome"}, {"index": 2, "title": "Amazing Grace"},
			{"index": 3, "title": "Announcements"}, {"index": 4, "title": "Sermon"},
		},
	}
)

func spec() addon.Spec {
	return addon.Spec{
		Manifest: addon.Manifest{
			ID: "proclaim", Name: "Proclaim", Version: Version, Publisher: "jadeck", APIVersion: addon.APIVersion, Repo: "toolingdev/jadeck-addon-proclaim",
			Description: "Next / previous, go to a service item, blank, and see what's on air.",
			Reaches:     "The Proclaim PCs you add as connections (Proclaim's network control, port 52195)",
			ConnectionTypes: []addon.ConnectionType{{
				ID: "proclaim", Label: "Proclaim", Capabilities: []string{addon.CapSlides},
				Description: "In Proclaim: Settings → Remote → Enable. The password is the Network Control Password there.",
				Fields: []addon.Field{
					{Key: "host", Label: "Proclaim PC address", Type: "text", Default: "127.0.0.1", Placeholder: "192.168.1.20", Required: true,
						Help: "127.0.0.1 when Proclaim runs on this same PC (no password needed then)."},
					{Key: "password", Label: "Network control password", Type: "password",
						Help: "Proclaim: Settings → Remote → Network Control Password. Not needed on the same PC."},
				},
				Sample:     map[string]any{"host": "192.168.1.20", "password": "••••••"},
				SampleTest: "Connected — on air, Sabbath Service",
			}},
			Actions: []addon.ActionDef{
				{ID: "go_to_item", Label: "Go to item…", MacroStep: true, Description: "Jump to a service item by its number (1 = first).",
					Params: []addon.Field{{Key: "index", Label: "Item number", Type: "number", Required: true, Min: addon.Num(1), Max: addon.Num(254)}}},
				{ID: "next_item", Label: "Next item", MacroStep: true},
				{ID: "prev_item", Label: "Previous item", MacroStep: true},
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
			},
			Changelog: "New: control Proclaim over the network — no MIDI or loopMIDI needed.",
		},
		Open: map[string]addon.OpenFunc{"proclaim": func(raw json.RawMessage) (any, error) { return Open(raw) }},
	}
}
