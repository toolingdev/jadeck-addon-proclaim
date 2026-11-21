package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/toolingdev/jadeck/sdk/addon"
)

func hostOfURL(u string) string { return strings.TrimPrefix(u, "http://") }

func TestScanHosts(t *testing.T) {
	hs := scanHosts([]netip.Prefix{
		netip.MustParsePrefix("192.168.1.37/24"),
		netip.MustParsePrefix("192.168.1.80/16"),    // same /24 again: once
		netip.MustParsePrefix("100.101.102.103/32"), // Tailscale: typed, not scanned
		netip.MustParsePrefix("169.254.10.2/16"),    // link-local
		netip.MustParsePrefix("127.0.0.1/8"),
		netip.MustParsePrefix("10.0.5.9/8"),
	})
	if len(hs) != 2*254 || hs[0] != "192.168.1.1" || hs[253] != "192.168.1.254" || hs[254] != "10.0.5.1" {
		t.Fatalf("%d hosts: %v … %v", len(hs), hs[:2], hs[len(hs)-2:])
	}
	for _, h := range hs {
		if strings.HasPrefix(h, "100.") || strings.HasPrefix(h, "169.254.") || strings.HasPrefix(h, "127.") {
			t.Fatalf("scanned %s", h)
		}
	}
}

func TestFindProclaim(t *testing.T) {
	on := newFake(t, true)
	off := newFake(t, false)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>router</html>")) }))
	defer other.Close()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	ln.Close()

	targets := []string{hostOfURL(on.URL), closed, hostOfURL(other.URL), hostOfURL(off.URL)}
	start := time.Now()
	opts := findProclaim(context.Background(), targets)
	if time.Since(start) > 3*time.Second {
		t.Fatalf("slow: %v", time.Since(start))
	}
	if len(opts) != 2 {
		t.Fatalf("%+v", opts)
	}
	if opts[0].Value != hostOfURL(on.URL) || opts[0].Label != opts[0].Value+" — Proclaim on this computer (on air: yes)" {
		t.Fatalf("on air: %+v", opts[0])
	}
	if opts[1].Value != hostOfURL(off.URL) || !strings.HasSuffix(opts[1].Label, "(on air: no)") {
		t.Fatalf("off air: %+v", opts[1])
	}
}

func TestFindProclaimCancelled(t *testing.T) {
	// Unroutable addresses would each wait out the timeout; a cancelled
	// scan returns at once.
	var targets []string
	for i := 1; i <= 254; i++ {
		targets = append(targets, hostPort(netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}).String(), Port))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if opts := findProclaim(ctx, targets); len(opts) != 0 {
		t.Fatalf("%+v", opts)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("cancelled scan took %v", time.Since(start))
	}
}

func TestDropSelf(t *testing.T) {
	opts := dropSelf([]addon.Option{{Value: "127.0.0.1"}, {Value: "192.168.1.37"}, {Value: "192.168.1.20"}}, map[string]bool{"192.168.1.37": true})
	if len(opts) != 2 || opts[1].Value != "192.168.1.20" {
		t.Fatalf("%+v", opts)
	}
}

func TestOptCache(t *testing.T) {
	var c optCache
	n := 0
	fn := func() []addon.Option { n++; return []addon.Option{} }
	c.get(fn)
	c.get(fn)
	if n != 1 {
		t.Fatalf("scanned %d times", n)
	}
	c.at = time.Now().Add(-scanCacheFor - time.Second)
	c.get(fn)
	if n != 2 {
		t.Fatalf("stale cache kept: %d", n)
	}
}

func TestTestMessages(t *testing.T) {
	// Port closed: Proclaim's remote is off.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	c, _ := Open([]byte(`{"host":"` + addr + `"}`))
	if i := c.Info(); i.Available || !strings.Contains(i.Target, "Settings → Remote → Local Server → Enable") {
		t.Fatalf("closed: %+v", i)
	}
	// Something else answers.
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	c, _ = Open([]byte(`{"host":"` + hostOfURL(other.URL) + `"}`))
	if i := c.Info(); i.Available || !strings.Contains(i.Target, "isn't Proclaim") {
		t.Fatalf("other: %+v", i)
	}
	// Wrong / missing network control password (another PC).
	f := newFake(t, false)
	c = open(t, f, "")
	if i := c.Info(); i.Available || !strings.Contains(i.Target, "Network Control Password") {
		t.Fatalf("no password: %+v", i)
	}
	c = open(t, f, "nope")
	if i := c.Info(); i.Available || !strings.Contains(i.Target, "refused") {
		t.Fatalf("wrong password: %+v", i)
	}
	c = open(t, f, "secret")
	if i := c.Info(); !i.Available {
		t.Fatalf("good: %+v", i)
	}
	// A name that doesn't resolve.
	c, _ = Open([]byte(`{"host":"no-such-host.invalid"}`))
	if i := c.Info(); i.Available || !strings.Contains(i.Target, "check the Proclaim PC's address") {
		t.Fatalf("dns: %+v", i)
	}
}
