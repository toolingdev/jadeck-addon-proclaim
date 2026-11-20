package main

import (
	"testing"

	"github.com/toolingdev/jadeck/sdk/addon/addontest"
)

func TestOverThePipe(t *testing.T) {
	h := addontest.Start(t, spec())
	h.Open("p", "proclaim", map[string]any{"host": "127.0.0.1"})
	var acts []string
	h.Call("p", "slides.actions", nil, &acts)
	if len(acts) != 5 {
		t.Fatalf("actions %v", acts)
	}
	if err := h.TryCall("p", "addon.action", map[string]any{"name": "go_to_item", "params": map[string]int{"index": 0}}, nil); err == nil {
		t.Fatal("item 0 accepted")
	}
}
