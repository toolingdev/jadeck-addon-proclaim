package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// fake is a tiny Proclaim local server.
func fake(t *testing.T, onAir bool) (*httptest.Server, *[]string) {
	var got []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /appCommand/authenticate", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Password string }
		json.NewDecoder(r.Body).Decode(&b)
		if b.Password != "secret" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"proclaimAuthToken":"tok123"}`))
	})
	mux.HandleFunc("GET /appCommand/perform", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ProclaimAuthToken") != "tok123" {
			w.WriteHeader(401)
			return
		}
		got = append(got, r.URL.Query().Get("appCommandName")+":"+r.URL.Query().Get("index"))
		w.Write([]byte("success"))
	})
	mux.HandleFunc("GET /onair/session", func(w http.ResponseWriter, r *http.Request) {
		if !onAir {
			w.WriteHeader(204)
			return
		}
		w.Write([]byte(`"sess-1"`))
	})
	mux.HandleFunc("GET /presentations/onair", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("OnAirSessionId") != "sess-1" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"id":"p","title":"Sabbath Service","localRevision":3,"serviceItems":[
			{"id":"a","title":"Welcome","kind":"Content","slides":[{"index":0}]},
			{"id":"b","title":"Amazing Grace","kind":"SongLyrics","slides":[{"index":0},{"index":1},{"index":2}]}]}`))
	})
	mux.HandleFunc("GET /onair/statusChanged", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"presentationLocalRevision":3,"status":{"itemId":"b","slideIndex":1,"quickScreenKind":"None"}}`))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s, &got
}

func open(t *testing.T, s *httptest.Server, pw string) *Conn {
	u, _ := url.Parse(s.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	c, err := Open(json.RawMessage(`{"host":"` + host + `","password":"` + pw + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	c.base = "http://" + net.JoinHostPort(host, port) // the fake isn't on 52195
	c.local = false                                   // exercise the login path
	_ = strconv.Itoa
	return c
}

func TestCommandsLogIn(t *testing.T) {
	s, got := fake(t, true)
	c := open(t, s, "secret")
	ctx := context.Background()
	if _, err := c.Do(ctx, "next"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddonAction(ctx, "go_to_item", json.RawMessage(`{"index":2}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*got, ",") != "NextSlide:,GoToServiceItem:2" {
		t.Fatalf("sent %v", *got)
	}
	if _, err := c.Do(ctx, "dance"); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestWrongPassword(t *testing.T) {
	s, _ := fake(t, true)
	c := open(t, s, "nope")
	if _, err := c.Do(context.Background(), "next"); err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("want a password error, got %v", err)
	}
}

func TestState(t *testing.T) {
	s, _ := fake(t, true)
	c := open(t, s, "secret")
	st, err := c.AddonState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st["onAir"] != true || st["currentIndex"] != 2 || st["itemTitle"] != "Amazing Grace" || st["slide"] != 2 || st["slides"] != 3 {
		t.Fatalf("state %v", st)
	}
	s2, _ := fake(t, false)
	st, _ = open(t, s2, "secret").AddonState(context.Background())
	if st["onAir"] != false || st["reachable"] != true {
		t.Fatalf("off air state %v", st)
	}
}
