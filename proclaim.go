// Package proclaim talks to Faithlife Proclaim over its network control
// (Proclaim: Settings → Remote → Enable; port 52195, fixed).
//
// Commands use Proclaim's documented App Command API
// (GET /appCommand/perform?appCommandName=NextSlide, with a token from
// POST /appCommand/authenticate when not on the same PC). On-air state
// and the service-item list come from the endpoints Proclaim's own
// remote apps use (/onair/session, /presentations/onair,
// /onair/statusChanged); they are not documented by Faithlife, so this
// driver treats them as best-effort: commands still work if they change.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/toolingdev/jadeck/sdk/addon"
)

const Port = 52195

type Config struct {
	Host     string `json:"host"`
	Password string `json:"password"`
}

// Conn is one Proclaim PC.
type Conn struct {
	base     string
	local    bool
	password string
	http     *http.Client

	mu    sync.Mutex
	token string
	pres  *presentation // cached service, by session + revision
	sess  string
	last  map[string]any
}

// Standard slide actions → Proclaim app commands.
var commands = map[string]string{
	"prev":  "PreviousSlide",
	"next":  "NextSlide",
	"black": "ShowBlankQuickScreen",
	"logo":  "ShowLogoQuickScreen",
	"clear": "ShowLastSlide",
}

func Open(raw json.RawMessage) (*Conn, error) {
	c := Config{Host: "127.0.0.1"}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	h := strings.TrimSpace(c.Host)
	if h == "" {
		return nil, fmt.Errorf("enter the Proclaim PC's address: %w", addon.ErrNotConfigured)
	}
	h = strings.TrimPrefix(strings.TrimPrefix(h, "http://"), "https://")
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	local := h == "127.0.0.1" || h == "localhost" || h == "::1"
	return &Conn{
		base: "http://" + net.JoinHostPort(h, strconv.Itoa(Port)), local: local, password: c.Password,
		http: &http.Client{Timeout: 4 * time.Second},
	}, nil
}

// ------------------------------------------------------------ transport

func (c *Conn) authenticate(ctx context.Context) (string, error) {
	pw := c.password
	if pw == "" {
		pw = "proclaim" // Proclaim's default network control password
	}
	body, _ := json.Marshal(map[string]string{"Password": pw})
	req, _ := http.NewRequestWithContext(ctx, "POST", c.base+"/appCommand/authenticate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", reach(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var out struct {
		Token string `json:"proclaimAuthToken"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(b, &out) != nil || out.Token == "" {
		return "", fmt.Errorf("Proclaim refused the network control password (Proclaim: Settings → Remote)")
	}
	c.mu.Lock()
	c.token = out.Token
	c.mu.Unlock()
	return out.Token, nil
}

// reach turns transport errors into something an operator can act on.
func reach(err error) error {
	var ne net.Error
	if errors.As(err, &ne) || strings.Contains(err.Error(), "connection refused") {
		return fmt.Errorf("Proclaim isn't answering — is it open with Settings → Remote → Enable on? (%v)", err)
	}
	return err
}

// Perform runs one Proclaim app command (index 1-254 for the commands
// that take one, 0 = none).
func (c *Conn) Perform(ctx context.Context, command string, index int) error {
	for attempt := 0; attempt < 2; attempt++ {
		c.mu.Lock()
		tok := c.token
		c.mu.Unlock()
		if !c.local && tok == "" {
			var err error
			if tok, err = c.authenticate(ctx); err != nil {
				return err
			}
		}
		q := url.Values{"appCommandName": {command}}
		if index > 0 {
			q.Set("index", strconv.Itoa(index))
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/appCommand/perform?"+q.Encode(), nil)
		if !c.local {
			req.Header.Set("ProclaimAuthToken", tok)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return reach(err)
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		if (resp.StatusCode == 401 || resp.StatusCode == 403) && !c.local && attempt == 0 {
			c.mu.Lock()
			c.token = "" // expired: log in again once
			c.mu.Unlock()
			continue
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("Proclaim answered %d to %s: %s", resp.StatusCode, command, strings.TrimSpace(string(b)))
		}
		return nil
	}
	return fmt.Errorf("Proclaim refused the network control password")
}

func (c *Conn) getJSON(ctx context.Context, path, session string, out any) (int, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+path, nil)
	req.Header.Set("Accept", "application/json")
	if session != "" {
		req.Header.Set("OnAirSessionId", session)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, reach(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 || len(bytes.TrimSpace(b)) == 0 {
		return resp.StatusCode, nil
	}
	if s, ok := out.(*string); ok {
		*s = strings.Trim(strings.TrimSpace(string(b)), `"`)
		return 200, nil
	}
	return 200, json.Unmarshal(b, out)
}

// --------------------------------------------------------------- state

type presentation struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	LocalRevision int    `json:"localRevision"`
	ServiceItems  []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Kind   string `json:"kind"`
		Slides []struct {
			Index int `json:"index"`
		} `json:"slides"`
	} `json:"serviceItems"`
}

type status struct {
	PresentationLocalRevision int `json:"presentationLocalRevision"`
	Status                    struct {
		ItemID          string `json:"itemId"`
		SlideIndex      int    `json:"slideIndex"`
		QuickScreenKind string `json:"quickScreenKind"`
		MediaState      string `json:"mediaState"`
	} `json:"status"`
}

// AddonState: on air, the service items and where Proclaim is.
func (c *Conn) AddonState(ctx context.Context) (map[string]any, error) {
	out := map[string]any{"reachable": false, "onAir": false, "items": []any{}, "currentIndex": 0}
	var sess string
	code, err := c.getJSON(ctx, "/onair/session", "", &sess)
	if err != nil {
		out["error"] = err.Error()
		return out, nil
	}
	out["reachable"] = true
	if code != 200 || sess == "" {
		c.mu.Lock()
		c.pres, c.sess = nil, ""
		c.mu.Unlock()
		return out, nil
	}
	out["onAir"] = true
	var st status
	c.getJSON(ctx, "/onair/statusChanged", sess, &st)
	c.mu.Lock()
	p := c.pres
	stale := p == nil || c.sess != sess || (st.PresentationLocalRevision != 0 && st.PresentationLocalRevision != p.LocalRevision)
	c.mu.Unlock()
	if stale {
		var np presentation
		if code, err := c.getJSON(ctx, "/presentations/onair", sess, &np); err == nil && code == 200 {
			p = &np
			c.mu.Lock()
			c.pres, c.sess = p, sess
			c.mu.Unlock()
		}
	}
	if p == nil {
		return out, nil
	}
	out["title"] = p.Title
	items := make([]map[string]any, 0, len(p.ServiceItems))
	for i, it := range p.ServiceItems {
		items = append(items, map[string]any{"index": i + 1, "title": it.Title, "kind": it.Kind, "slides": len(it.Slides)})
		if it.ID == st.Status.ItemID {
			out["currentIndex"] = i + 1
			out["itemTitle"] = it.Title
			out["slide"] = st.Status.SlideIndex + 1
			out["slides"] = len(it.Slides)
		}
	}
	out["items"] = items
	if q := st.Status.QuickScreenKind; q != "" && q != "None" {
		out["quickScreen"] = q
	}
	return out, nil
}

// -------------------------------------------------------------- slides

func (c *Conn) Info() addon.PresentationInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	var sess string
	_, err := c.getJSON(ctx, "/onair/session", "", &sess)
	info := addon.PresentationInfo{Available: err == nil, Target: "Proclaim at " + strings.TrimPrefix(c.base, "http://")}
	if err != nil {
		info.Target = err.Error()
	}
	c.mu.Lock()
	if c.last != nil {
		info.Details = map[string]any{"lastSent": c.last}
	}
	c.mu.Unlock()
	return info
}

func (c *Conn) Actions() []string { return []string{"prev", "next", "black", "logo", "clear"} }

func (c *Conn) Do(ctx context.Context, action string) (map[string]any, error) {
	cmd, ok := commands[action]
	if !ok {
		return nil, fmt.Errorf("%w %q", addon.ErrUnknownAction, action)
	}
	if err := c.Perform(ctx, cmd, 0); err != nil {
		return nil, err
	}
	det := map[string]any{"action": action, "command": cmd, "at": time.Now().Format(time.RFC3339)}
	c.mu.Lock()
	c.last = det
	c.mu.Unlock()
	return det, nil
}

// ------------------------------------------------------------- actions

// AddonAction runs the Proclaim add-on's own actions.
func (c *Conn) AddonAction(ctx context.Context, name string, params json.RawMessage) (any, error) {
	var p struct {
		Index int `json:"index"`
	}
	if len(params) > 0 && string(params) != "null" {
		json.Unmarshal(params, &p)
	}
	simple := map[string]string{
		"next_item": "NextServiceItem", "prev_item": "PreviousServiceItem",
		"go_on_air": "GoOnAir", "go_off_air": "GoOffAir",
		"start_service": "StartService", "start_pre_service": "StartPreService",
	}
	switch name {
	case "go_to_item":
		if p.Index < 1 || p.Index > 254 {
			return nil, fmt.Errorf("item number must be 1-254")
		}
		return map[string]any{"ok": true}, c.Perform(ctx, "GoToServiceItem", p.Index)
	case "next", "prev", "black", "logo", "clear":
		return c.Do(ctx, name)
	}
	if cmd, ok := simple[name]; ok {
		return map[string]any{"ok": true}, c.Perform(ctx, cmd, 0)
	}
	return nil, fmt.Errorf("%w %q", addon.ErrUnknownAction, name)
}
