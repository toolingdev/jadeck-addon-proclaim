// Package proclaim talks to Faithlife Proclaim over its Local Remote
// server (Proclaim: Settings → Remote → Enable; port 52195, fixed).
//
// Two ways in, as Proclaim's own remote apps use them:
//
//   - The remote-control API: POST /auth/control (a fixed password built
//     into Proclaim) gives a token for POST /onair/statusChanged, which
//     goes to an exact service item / slide, quick screen or media state.
//     It only works while a presentation is on air.
//   - App commands: GET /appCommand/perform?appCommandName=NextSlide, with
//     a token from POST /appCommand/authenticate (the user's Network
//     Control Password) unless on the same PC. Fire-and-forget.
//
// On-air state comes from /onair/session, /onair/statusChanged (long
// polled) and /presentations/onair. See docs/PROCLAIM-API.md.
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

const (
	Port = 52195
	// Proclaim leaves custom quick screens out unless the client says it
	// is its own Local Remote app.
	userAgent = "ProclaimLocalRemote/1.4.0.0"
	// controlPassword is the password of Proclaim's remote-control login
	// (/auth/control). It is a constant in Proclaim's code, the same on
	// every install, not a user secret.
	controlPassword = "Proclaim"
	clientName      = "jadeck"
)

type Config struct {
	Host     string `json:"host"`
	Password string `json:"password"`
}

// Conn is one Proclaim PC.
type Conn struct {
	base     string
	local    bool
	password string
	http     *http.Client  // ordinary requests
	poll     *http.Client  // long polls (Proclaim holds them up to 60 s)
	retry    time.Duration // status loop back-off
	confirm  time.Duration // how long to wait for Proclaim to show a change

	mu      sync.Mutex
	token   string        // app-command token
	ctl     controlAuth   // remote-control login
	pres    *presentation // cached service
	presFor int64         // the status's presentationLocalRevision pres was fetched for
	sess    string        // the session pres belongs to
	last    map[string]any
	recent  []string // Bible references shown from here, newest first
	images  imageCache

	notify  func()
	stop    func()
	watched watchState

	// stateSnap's shared snapshot (settings.go)
	snapC   snapshot
	snapErr error
	snapAt  time.Time
	snapFor *statusUpdate
}

type controlAuth struct{ sess, token, conn string }

// watchState is what the status loop last saw.
type watchState struct {
	ok   bool // the last round worked
	sess string
	st   *statusUpdate
}

// Standard slide actions → Proclaim app commands.
var commands = map[string]string{
	"prev":  "PreviousSlide",
	"next":  "NextSlide",
	"black": "ShowBlankQuickScreen",
	"logo":  "ShowLogoQuickScreen",
	"clear": "ShowLastSlide",
}

var (
	errOffAir    = errors.New("Proclaim isn't on air")
	errNoControl = errors.New("Proclaim's remote-control login didn't work")
)

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
	h = strings.TrimSuffix(h, "/")
	// Proclaim always listens on 52195; "host:port" is accepted for
	// stand-ins such as jadeck's simulator.
	port := Port
	if host, p, err := net.SplitHostPort(h); err == nil {
		h = host
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			port = n
		}
	}
	h = strings.Trim(h, "[]")
	local := h == "127.0.0.1" || h == "localhost" || h == "::1"
	return &Conn{
		base: "http://" + net.JoinHostPort(h, strconv.Itoa(port)), local: local, password: c.Password,
		http:  &http.Client{Timeout: 4 * time.Second},
		poll:  &http.Client{Timeout: 75 * time.Second},
		retry: 2 * time.Second, confirm: 1500 * time.Millisecond,
	}, nil
}

// ------------------------------------------------------------ transport

// send does one request and reads the answer. Headers with empty values
// are left out.
func (c *Conn) send(ctx context.Context, cl *http.Client, method, path string, body any, hdr map[string]string) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, nil, reach(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, nil, reach(err)
	}
	return resp.StatusCode, b, nil
}

// reach turns transport errors into something an operator can act on.
func reach(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	var ne net.Error
	if errors.As(err, &ne) || strings.Contains(err.Error(), "connection refused") {
		return fmt.Errorf("Proclaim isn't answering — is it open with Settings → Remote → Local Server → Enable on? (%w)", err)
	}
	return err
}

// apiError is Proclaim's {"errorCode": "..."} as " (ItemNotFound)", or "".
func apiError(b []byte) string {
	var e struct {
		ErrorCode string `json:"errorCode"`
	}
	if json.Unmarshal(b, &e) == nil && e.ErrorCode != "" {
		return " (" + e.ErrorCode + ")"
	}
	return ""
}

// ------------------------------------------------------- app commands

func (c *Conn) authenticate(ctx context.Context) (string, error) {
	pw := c.password
	if pw == "" {
		pw = "proclaim" // Proclaim's default network control password
	}
	code, b, err := c.send(ctx, c.http, "POST", "/appCommand/authenticate", map[string]string{"Password": pw}, nil)
	if err != nil {
		return "", err
	}
	switch code {
	case 200:
	case 400:
		return "", fmt.Errorf("Proclaim wants a network control password (Proclaim: Settings → Remote → Network Control Password)")
	case 403:
		if c.password == "" {
			return "", fmt.Errorf("Proclaim wants its network control password — enter it here (Proclaim: Settings → Remote → Network Control Password)")
		}
		return "", fmt.Errorf("Proclaim refused the network control password — check it matches Proclaim: Settings → Remote → Network Control Password")
	default:
		return "", fmt.Errorf("Proclaim answered %d to the network control login%s", code, apiError(b))
	}
	var out struct {
		Token string `json:"proclaimAuthToken"`
	}
	if json.Unmarshal(b, &out) != nil || out.Token == "" {
		return "", fmt.Errorf("Proclaim's network control login sent no token")
	}
	c.mu.Lock()
	c.token = out.Token
	c.mu.Unlock()
	return out.Token, nil
}

// Perform runs one Proclaim app command (index 1-254 for the commands
// that take one, 0 = none). Proclaim runs them fire-and-forget: success
// means it accepted the command, not that anything changed.
func (c *Conn) Perform(ctx context.Context, command string, index int) error {
	for attempt := 0; attempt < 2; attempt++ {
		c.mu.Lock()
		tok := c.token
		c.mu.Unlock()
		// The same PC needs no token, unless Proclaim asked for one.
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
		code, b, err := c.send(ctx, c.http, "GET", "/appCommand/perform?"+q.Encode(), nil, map[string]string{"ProclaimAuthToken": tok})
		if err != nil {
			return err
		}
		switch code {
		case 200:
			return nil
		case 401:
			// No token, or Proclaim restarted / the password changed
			// since we logged in: log in once more.
			c.mu.Lock()
			c.token = ""
			c.mu.Unlock()
			if attempt == 0 {
				if _, err := c.authenticate(ctx); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("Proclaim keeps rejecting the network control login for %s — check the Network Control Password (Proclaim: Settings → Remote)", command)
		case 404:
			return fmt.Errorf("Proclaim doesn't know the command %s", command)
		default:
			return fmt.Errorf("Proclaim answered %d to %s: %s", code, command, strings.TrimSpace(string(b)))
		}
	}
	return fmt.Errorf("Proclaim rejected the network control login for %s", command)
}

// ---------------------------------------------------- on-air protocol

type slideRef struct {
	LocalRevision int64 `json:"localRevision"` // the item's layout version: changes when its slides do
	Index         int   `json:"index"`
}

type serviceItem struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Notes  string     `json:"notes"` // HTML
	Kind   string     `json:"kind"`
	Slides []slideRef `json:"slides"`
}

// quickScreen is one of the presentation's quick screens. Custom ones
// (made in the service) have a service item and a title; Proclaim's
// built-in ones only a kind.
type quickScreen struct {
	ID            string  `json:"id"`
	ServiceItemID *string `json:"serviceItemId"`
	Kind          string  `json:"kind"`
	Title         string  `json:"title"`
}

type presentation struct {
	ID                    string        `json:"id"`
	Title                 string        `json:"title"`
	LocalRevision         int64         `json:"localRevision"`
	StartTime             int64         `json:"startTime"` // .NET ticks, UTC
	WarmupStartIndex      int           `json:"warmupStartIndex"`
	ServiceStartIndex     int           `json:"serviceStartIndex"`
	PostServiceStartIndex int           `json:"postServiceStartIndex"`
	ServiceItems          []serviceItem `json:"serviceItems"`
	QuickScreens          []quickScreen `json:"quickScreens"` // null unless we say we're Proclaim's remote (User-Agent)
}

// section names the part of the service item i (0-based) is in.
func (p *presentation) section(i int) string {
	switch {
	case p.PostServiceStartIndex > p.ServiceStartIndex && i >= p.PostServiceStartIndex:
		return "post-service"
	case i >= p.ServiceStartIndex:
		return "service"
	case i >= p.WarmupStartIndex && p.WarmupStartIndex < p.ServiceStartIndex:
		return "warm-up"
	}
	return "pre-service"
}

// appCommandIndex is the GoToServiceItem index for item i (0-based):
// Proclaim counts from the service start and skips items without slides.
func (p *presentation) appCommandIndex(i int) (int, error) {
	start := max(p.ServiceStartIndex, 0)
	if i < start {
		return 0, fmt.Errorf("item %d is before the service start, which Proclaim's GoToServiceItem command can't reach", i+1)
	}
	k := 0
	for j := start; j <= i && j < len(p.ServiceItems); j++ {
		if len(p.ServiceItems[j].Slides) > 0 {
			k++
		}
	}
	if k < 1 || k > 254 {
		return 0, fmt.Errorf("item %d can't be reached with Proclaim's GoToServiceItem command", i+1)
	}
	return k, nil
}

type onAirStatus struct {
	Revision        int64   `json:"revision"` // the on-air step
	ItemID          string  `json:"itemId"`
	SlideIndex      int     `json:"slideIndex"`
	QuickScreen     *string `json:"quickScreen"`
	QuickScreenData *string `json:"quickScreenData"`
	QuickScreenKind string  `json:"quickScreenKind"`
	MediaState      *string `json:"mediaState"`
}

type statusUpdate struct {
	PresentationID            string      `json:"presentationId"`
	PresentationLocalRevision int64       `json:"presentationLocalRevision"`
	Status                    onAirStatus `json:"status"`
}

func (s *statusUpdate) media() string {
	if s == nil || s.Status.MediaState == nil {
		return ""
	}
	return *s.Status.MediaState
}

func sameStatus(a, b *statusUpdate) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// session is the on-air session id, "" when Proclaim is off air.
func (c *Conn) session(ctx context.Context) (string, error) {
	code, b, err := c.send(ctx, c.http, "GET", "/onair/session", nil, nil)
	if err != nil {
		return "", err
	}
	switch code {
	case 200:
		return strings.Trim(strings.TrimSpace(string(b)), `"`), nil
	case 204:
		return "", nil
	}
	return "", fmt.Errorf("Proclaim answered %d to /onair/session", code)
}

// getStatus reads the on-air status. With since, Proclaim holds the
// request (up to 60 s) until the status moves on from it. A non-200 code
// with a nil error means Proclaim went off air.
func (c *Conn) getStatus(ctx context.Context, cl *http.Client, sess string, since *statusUpdate, connID string) (*statusUpdate, int, error) {
	path := "/onair/statusChanged"
	if since != nil {
		path += "?" + url.Values{
			"localRevision": {strconv.FormatInt(since.PresentationLocalRevision, 10)},
			"step":          {strconv.FormatInt(since.Status.Revision, 10)},
		}.Encode()
	}
	code, b, err := c.send(ctx, cl, "GET", path, nil, map[string]string{"OnAirSessionId": sess, "ConnectionId": connID})
	if err != nil {
		return nil, 0, err
	}
	if code != 200 {
		return nil, code, nil
	}
	var st statusUpdate
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, code, fmt.Errorf("Proclaim sent a status we couldn't read: %w", err)
	}
	return &st, 200, nil
}

// presentationFor is the on-air service, fetched again only when the
// session or the status's presentation revision changes. The revision is
// the status's, remembered at fetch time: after a forced update the
// presentation's own localRevision can differ from it for good.
func (c *Conn) presentationFor(ctx context.Context, sess string, st *statusUpdate) (*presentation, error) {
	c.mu.Lock()
	p := c.pres
	fresh := p != nil && c.sess == sess && c.presFor == st.PresentationLocalRevision
	c.mu.Unlock()
	if fresh {
		return p, nil
	}
	code, b, err := c.send(ctx, c.http, "GET", "/presentations/onair", nil, map[string]string{"OnAirSessionId": sess})
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("Proclaim answered %d to /presentations/onair%s", code, apiError(b))
	}
	var np presentation
	if err := json.Unmarshal(b, &np); err != nil {
		return nil, fmt.Errorf("Proclaim sent a service we couldn't read: %w", err)
	}
	c.mu.Lock()
	c.pres, c.sess, c.presFor = &np, sess, st.PresentationLocalRevision
	c.mu.Unlock()
	return &np, nil
}

type snapshot struct {
	sess string
	st   *statusUpdate
	p    *presentation
}

// snap reads where Proclaim is; sess is "" when it is off air. With
// useWatched, the status loop's last status stands in for a fresh read
// while it watches the same session.
func (c *Conn) snap(ctx context.Context, useWatched bool) (snapshot, error) {
	sess, err := c.session(ctx)
	if err != nil {
		return snapshot{}, err
	}
	if sess == "" {
		c.mu.Lock()
		c.pres, c.sess, c.presFor = nil, "", 0
		c.mu.Unlock()
		return snapshot{}, nil
	}
	var st *statusUpdate
	if useWatched {
		c.mu.Lock()
		if w := c.watched; w.ok && w.sess == sess {
			st = w.st
		}
		c.mu.Unlock()
	}
	if st == nil {
		var code int
		if st, code, err = c.getStatus(ctx, c.http, sess, nil, ""); err != nil {
			return snapshot{sess: sess}, err
		}
		if code != 200 {
			return snapshot{}, nil // went off air just now
		}
	}
	p, err := c.presentationFor(ctx, sess, st)
	return snapshot{sess, st, p}, err
}

// control logs in to Proclaim's remote control for this session.
func (c *Conn) control(ctx context.Context, sess string, renew bool) (controlAuth, error) {
	c.mu.Lock()
	a := c.ctl
	c.mu.Unlock()
	if !renew && a.token != "" && a.sess == sess {
		return a, nil
	}
	body := map[string]any{"userName": clientName, "remoteDeviceName": clientName, "password": controlPassword}
	code, b, err := c.send(ctx, c.http, "POST", "/auth/control", body, map[string]string{"OnAirSessionId": sess})
	if err != nil {
		return controlAuth{}, err
	}
	if code != 200 {
		return controlAuth{}, fmt.Errorf("%w: Proclaim answered %d%s", errNoControl, code, apiError(b))
	}
	var out struct {
		Token string `json:"authenticationToken"`
		Conn  string `json:"connectionId"`
	}
	if json.Unmarshal(b, &out) != nil || out.Token == "" {
		return controlAuth{}, fmt.Errorf("%w: no token in the answer", errNoControl)
	}
	a = controlAuth{sess: sess, token: out.Token, conn: out.Conn}
	c.mu.Lock()
	c.ctl = a
	c.mu.Unlock()
	return a, nil
}

// postStatus asks Proclaim to show a status: edit gets the current one
// and returns what to show. Errors wrapping errNoControl mean the
// remote-control route isn't available (fall back to app commands).
func (c *Conn) postStatus(ctx context.Context, sess string, edit func(cur statusUpdate) statusUpdate) error {
	a, err := c.control(ctx, sess, false)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		cur, code, err := c.getStatus(ctx, c.http, sess, nil, "")
		if err != nil {
			return err
		}
		if code != 200 {
			return errOffAir
		}
		next := edit(*cur)
		// The current step: Proclaim ignores one lower than its own.
		next.Status.Revision = cur.Status.Revision
		code, b, err := c.send(ctx, c.http, "POST", "/onair/statusChanged", next,
			map[string]string{"OnAirSessionId": sess, "ProclaimAuthToken": a.token, "ConnectionId": a.conn})
		if err != nil {
			return err
		}
		switch code {
		case 200:
			return nil
		case 403:
			if attempt == 0 { // Proclaim restarted since the login
				if a, err = c.control(ctx, sess, true); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("%w: Proclaim refused the remote-control token", errNoControl)
		case 404:
			if e := apiError(b); e != "" && e != " (PresentationNotOnAir)" {
				return fmt.Errorf("Proclaim couldn't find that%s", e)
			}
			return errOffAir
		default:
			return fmt.Errorf("Proclaim answered %d%s", code, apiError(b))
		}
	}
	return fmt.Errorf("%w: Proclaim refused the remote-control token", errNoControl)
}

// waitFor reads the status until ok says Proclaim shows what was asked,
// for up to c.confirm.
func (c *Conn) waitFor(ctx context.Context, sess string, ok func(*statusUpdate) bool) (*statusUpdate, bool) {
	deadline := time.Now().Add(c.confirm)
	for {
		st, code, err := c.getStatus(ctx, c.http, sess, nil, "")
		if err == nil && code == 200 && ok(st) {
			return st, true
		}
		if time.Now().After(deadline) || !sleep(ctx, 100*time.Millisecond) {
			return st, false
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// --------------------------------------------------------- status loop

// SetNotify starts the status loop: it long-polls Proclaim and tells the
// controller when what's on air changes.
func (c *Conn) SetNotify(n addon.Notifier) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notify = n
	if c.stop != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.stop = func() { cancel(); <-done }
	go func() {
		defer close(done)
		c.watch(ctx)
	}()
}

// Close stops the status loop and lets go of the remote-control login.
func (c *Conn) Close() error {
	c.mu.Lock()
	stop, a := c.stop, c.ctl
	c.stop, c.ctl = nil, controlAuth{}
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
	if a.conn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		c.send(ctx, c.http, "GET", "/auth/disconnectcontrol", nil, map[string]string{"OnAirSessionId": a.sess, "ConnectionId": a.conn})
	}
	return nil
}

func (c *Conn) changed() {
	c.mu.Lock()
	n := c.notify
	c.mu.Unlock()
	if n != nil {
		n()
	}
}

func (c *Conn) setWatched(w watchState) {
	c.mu.Lock()
	c.watched = w
	c.mu.Unlock()
}

func (c *Conn) watch(ctx context.Context) {
	var (
		last     *statusUpdate
		lastSess string
		seen     bool   // something was reported yet
		tried    string // session the remote-control login was tried for
	)
	for ctx.Err() == nil {
		sess, err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil || sess == "" {
			c.setWatched(watchState{ok: err == nil})
			if !seen || lastSess != "" {
				c.changed()
			}
			seen, lastSess, last = true, "", nil
			sleep(ctx, c.retry)
			continue
		}
		if sess != lastSess {
			last = nil
		}
		// A ConnectionId on the polls lists us as a controller in Proclaim.
		if tried != sess {
			tried = sess
			c.control(ctx, sess, false)
		}
		var connID string
		c.mu.Lock()
		if c.ctl.sess == sess {
			connID = c.ctl.conn
		}
		c.mu.Unlock()
		began := time.Now()
		st, code, err := c.getStatus(ctx, c.poll, sess, last, connID)
		if ctx.Err() != nil {
			return
		}
		if err != nil || code != 200 {
			c.setWatched(watchState{})
			if !seen || lastSess != "" {
				c.changed()
			}
			seen, lastSess, last = true, "", nil
			sleep(ctx, c.retry)
			continue
		}
		moved := !seen || sess != lastSess || !sameStatus(last, st)
		seen, lastSess, last = true, sess, st
		c.setWatched(watchState{ok: true, sess: sess, st: st})
		if moved {
			c.changed()
		} else if time.Since(began) < 250*time.Millisecond {
			sleep(ctx, c.retry/4) // Proclaim isn't holding the poll: don't spin
		}
	}
}

// -------------------------------------------------------------- slides

func (c *Conn) Info() addon.PresentationInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, err := c.session(ctx)
	if err == nil {
		err = c.checkPassword(ctx)
	}
	info := addon.PresentationInfo{Available: err == nil, Target: "Proclaim at " + strings.TrimPrefix(c.base, "http://")}
	if err != nil {
		info.Target = c.explain(err)
	}
	c.mu.Lock()
	if c.last != nil {
		info.Details = map[string]any{"lastSent": c.last}
	}
	c.mu.Unlock()
	return info
}

func (c *Conn) Actions() []string { return []string{"prev", "next", "black", "logo", "clear"} }

// shows says how the status looks after the quick-screen actions whose
// outcome is certain; the others aren't checked.
var shows = map[string]func(*statusUpdate) bool{
	"black": func(s *statusUpdate) bool { return s.Status.QuickScreenKind == "Blank" },
	"clear": func(s *statusUpdate) bool {
		return s.Status.QuickScreenKind == "" || s.Status.QuickScreenKind == "None"
	},
	"show_no_text": func(s *statusUpdate) bool { return s.Status.QuickScreenKind == "NoText" },
	"show_text":    func(s *statusUpdate) bool { return s.Status.QuickScreenKind != "NoText" },
}

func (c *Conn) Do(ctx context.Context, action string) (map[string]any, error) {
	cmd, ok := commands[action]
	if !ok {
		return nil, fmt.Errorf("%w %q", addon.ErrUnknownAction, action)
	}
	if err := c.Perform(ctx, cmd, 0); err != nil {
		return nil, err
	}
	det := map[string]any{"action": action, "command": cmd, "at": time.Now().Format(time.RFC3339)}
	c.check(ctx, det, shows[action])
	c.mu.Lock()
	c.last = det
	c.mu.Unlock()
	return det, nil
}

// check reads the status back after a fire-and-forget command and notes
// in det whether Proclaim shows what was asked (when that is knowable and
// Proclaim is on air).
func (c *Conn) check(ctx context.Context, det map[string]any, ok func(*statusUpdate) bool) {
	if ok == nil {
		return
	}
	sess, err := c.session(ctx)
	if err != nil || sess == "" {
		return
	}
	_, shown := c.waitFor(ctx, sess, ok)
	det["confirmed"] = shown
}

// ------------------------------------------------------------- actions

// appCommands are add-on actions that are one Proclaim app command.
var appCommands = map[string]string{
	"next_item": "NextServiceItem", "prev_item": "PreviousServiceItem",
	"next_slide": "NextSlide", "prev_slide": "PreviousSlide",
	"go_on_air": "GoOnAir", "go_off_air": "GoOffAir",
	"start_pre_service": "StartPreService", "start_warm_up": "StartWarmUp",
	"start_service": "StartService", "start_post_service": "StartPostService",
	"show_no_text": "ShowNoTextQuickScreen", "show_text": "ShowText",
	"video_restart": "VideoRestart", "video_rewind": "VideoRewind", "video_forward": "VideoFastForward",
	"next_audio": "NextAudioItem",
}

// actionParams is every add-on action's parameters.
type actionParams struct {
	Index     int             `json:"index"`     // go_to_item, go_to_slide: 1-based
	ID        string          `json:"id"`        // go_to_item: the item's id; quick_screen: a custom quick screen's id
	Slide     json.RawMessage `json:"slide"`     // go_to_slide: 1-based, number or text (the slide strip sends text)
	Reference string          `json:"reference"` // show_bible
	Kind      string          `json:"kind"`      // quick_screen
	Part      string          `json:"part"`      // song_part
	Number    int             `json:"number"`    // song_part: 1-based
}

// AddonAction runs the Proclaim add-on's own actions.
func (c *Conn) AddonAction(ctx context.Context, name string, params json.RawMessage) (any, error) {
	var p actionParams
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("bad parameters for %s: %v", name, err)
		}
	}
	switch name {
	case "go_to_item":
		if id := strings.TrimSpace(p.ID); id != "" {
			return c.goToItemID(ctx, id)
		}
		if p.Index < 1 || p.Index > 254 {
			return nil, fmt.Errorf("item number must be 1-254")
		}
		return c.goToItem(ctx, p.Index)
	case "go_to_slide":
		n := p.Index
		if len(p.Slide) > 0 && string(p.Slide) != "null" {
			v, err := strconv.Atoi(strings.Trim(string(p.Slide), `" `))
			if err != nil {
				return nil, fmt.Errorf("slide must be a number")
			}
			n = v
		}
		if n < 1 {
			return nil, fmt.Errorf("slide number must be 1 or more")
		}
		return c.goToSlide(ctx, n)
	case "take_next":
		return c.takeNext(ctx)
	case "show_bible":
		ref := strings.TrimSpace(p.Reference)
		if ref == "" {
			return nil, fmt.Errorf("enter a Bible reference, e.g. John 3:16")
		}
		return c.showBible(ctx, ref)
	case "quick_screen":
		return c.quickScreen(ctx, p.Kind, p.ID)
	case "song_part":
		return c.songPart(ctx, p.Part, p.Number)
	case "video_play":
		return c.video(ctx, true)
	case "video_pause":
		return c.video(ctx, false)
	case "next", "prev", "black", "logo", "clear":
		return c.Do(ctx, name)
	}
	if cmd, ok := appCommands[name]; ok {
		if err := c.Perform(ctx, cmd, 0); err != nil {
			return nil, err
		}
		res := map[string]any{"ok": true}
		c.check(ctx, res, shows[name])
		return res, nil
	}
	return nil, fmt.Errorf("%w %q", addon.ErrUnknownAction, name)
}

// onAir is the current snapshot, or errOffAir.
func (c *Conn) onAir(ctx context.Context) (snapshot, error) {
	s, err := c.snap(ctx, false)
	if err != nil {
		return s, err
	}
	if s.sess == "" || s.st == nil || s.p == nil {
		return s, errOffAir
	}
	return s, nil
}

// goToItem goes to item n (1-based) of the list AddonState shows: every
// item, pre-service ones included.
func (c *Conn) goToItem(ctx context.Context, n int) (map[string]any, error) {
	s, err := c.onAir(ctx)
	if err != nil {
		return nil, err
	}
	if n > len(s.p.ServiceItems) {
		return nil, fmt.Errorf("there is no item %d: the service has %d", n, len(s.p.ServiceItems))
	}
	it := s.p.ServiceItems[n-1]
	if len(it.Slides) == 0 {
		return nil, fmt.Errorf("item %d (%s) has no slides to show", n, it.Title)
	}
	res := map[string]any{"ok": true, "index": n, "title": it.Title}
	err = c.postStatus(ctx, s.sess, func(cur statusUpdate) statusUpdate {
		cur.Status = onAirStatus{ItemID: it.ID, SlideIndex: 0, QuickScreenKind: "None"}
		return cur
	})
	if errors.Is(err, errNoControl) {
		k, kerr := s.p.appCommandIndex(n - 1)
		if kerr != nil {
			return nil, fmt.Errorf("%v, and %w", err, kerr)
		}
		if err := c.Perform(ctx, "GoToServiceItem", k); err != nil {
			return nil, err
		}
		res["via"] = "app command"
	} else if err != nil {
		return nil, err
	}
	if _, ok := c.waitFor(ctx, s.sess, func(st *statusUpdate) bool { return st.Status.ItemID == it.ID }); !ok {
		return nil, fmt.Errorf("Proclaim didn't go to item %d (%s)", n, it.Title)
	}
	return res, nil
}

// goToSlide goes to slide n (1-based) of the current item.
func (c *Conn) goToSlide(ctx context.Context, n int) (map[string]any, error) {
	s, err := c.onAir(ctx)
	if err != nil {
		return nil, err
	}
	item := s.st.Status.ItemID
	var cur *serviceItem
	for i := range s.p.ServiceItems {
		if s.p.ServiceItems[i].ID == item {
			cur = &s.p.ServiceItems[i]
		}
	}
	if cur == nil {
		return nil, fmt.Errorf("Proclaim isn't showing a service item")
	}
	if n > len(cur.Slides) {
		return nil, fmt.Errorf("there is no slide %d: %s has %d", n, cur.Title, len(cur.Slides))
	}
	res := map[string]any{"ok": true, "slide": n, "title": cur.Title}
	err = c.postStatus(ctx, s.sess, func(st statusUpdate) statusUpdate {
		st.Status = onAirStatus{ItemID: item, SlideIndex: n - 1, QuickScreenKind: "None"}
		return st
	})
	if errors.Is(err, errNoControl) {
		if n > 254 {
			return nil, fmt.Errorf("%v, and Proclaim's GoToSlide command only reaches slide 254", err)
		}
		if err := c.Perform(ctx, "GoToSlide", n); err != nil {
			return nil, err
		}
		res["via"] = "app command"
	} else if err != nil {
		return nil, err
	}
	if _, ok := c.waitFor(ctx, s.sess, func(st *statusUpdate) bool {
		return st.Status.ItemID == item && st.Status.SlideIndex == n-1
	}); !ok {
		return nil, fmt.Errorf("Proclaim didn't go to slide %d", n)
	}
	return res, nil
}

// showBible shows a passage on Proclaim's on-screen Bible quick screen.
// Only the remote-control route can do this.
//
// There is no way to pick the Bible version from here. Proclaim (4.22,
// MainWindowViewModel.ProcessRemoteRequest) puts quickScreenData into the
// on-screen Bible's search box and presses Go; the text is parsed by
// Faithlife's reference parser (BibliaService.TryParseBiblePassage) with
// the Bible chosen in Proclaim's On-screen Bible quick screen, which is
// also the version shown. That Bible's language sets which book names
// parse ("Juan 3:16" needs a Spanish Bible selected). A version
// abbreviation in the text ("John 3:16 ESV") is not a version switch.
func (c *Conn) showBible(ctx context.Context, ref string) (map[string]any, error) {
	s, err := c.onAir(ctx)
	if err != nil {
		return nil, err
	}
	err = c.postStatus(ctx, s.sess, func(cur statusUpdate) statusUpdate {
		cur.Status = onAirStatus{ItemID: cur.Status.ItemID, SlideIndex: cur.Status.SlideIndex, QuickScreenKind: "OnScreenBible", QuickScreenData: &ref}
		return cur
	})
	if err != nil {
		return nil, err
	}
	if _, ok := c.waitFor(ctx, s.sess, func(st *statusUpdate) bool { return st.Status.QuickScreenKind == "OnScreenBible" }); !ok {
		return nil, fmt.Errorf("Proclaim didn't show %s", ref)
	}
	c.remember(ref)
	return map[string]any{"ok": true, "reference": ref}, nil
}

// video plays or pauses the video on screen.
func (c *Conn) video(ctx context.Context, play bool) (map[string]any, error) {
	s, err := c.onAir(ctx)
	if err != nil {
		return nil, err
	}
	state, cmd := "Paused", "VideoPause"
	if play {
		state, cmd = "Playing", "VideoPlay"
	}
	res := map[string]any{"ok": true}
	err = c.postStatus(ctx, s.sess, func(cur statusUpdate) statusUpdate {
		cur.Status.MediaState = &state // same item and slide
		return cur
	})
	if errors.Is(err, errNoControl) {
		if err := c.Perform(ctx, cmd, 0); err != nil {
			return nil, err
		}
		res["via"] = "app command"
	} else if err != nil {
		return nil, err
	}
	if _, ok := c.waitFor(ctx, s.sess, func(st *statusUpdate) bool { return (st.media() == "Playing") == play }); !ok {
		if play {
			return nil, fmt.Errorf("Proclaim didn't start playing — is a video on screen?")
		}
		return nil, fmt.Errorf("Proclaim didn't pause the video")
	}
	return res, nil
}
