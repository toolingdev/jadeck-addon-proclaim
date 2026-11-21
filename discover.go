package main

// "Find on network" for the host field (option source proclaim.hosts):
// Proclaim PCs on this computer's networks whose Local Server answers
// GET / with "Hello from Proclaim Local Remote", plus this computer.
// Read-only: GET / and GET /onair/session, nothing else.

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/toolingdev/jadeck/sdk/addon"
)

const (
	optHosts   = "proclaim.hosts"
	helloProbe = "Hello from Proclaim Local Remote"
)

var hostCache optCache

// hostOptions is the proclaim.hosts option source.
func hostOptions() []addon.Option {
	return hostCache.get(func() []addon.Option {
		ctx, cancel := context.WithTimeout(context.Background(), scanBudget)
		defer cancel()
		addrs := localAddrs()
		targets := []string{hostPort("127.0.0.1", Port)}
		for _, h := range scanHosts(addrs) {
			targets = append(targets, hostPort(h, Port))
		}
		return dropSelf(findProclaim(ctx, targets), selfHosts(addrs))
	})
}

// dropSelf leaves out this computer's own LAN address when 127.0.0.1
// already lists it.
func dropSelf(opts []addon.Option, self map[string]bool) []addon.Option {
	local := false
	for _, o := range opts {
		if strings.HasPrefix(o.Value, "127.0.0.1") {
			local = true
		}
	}
	if !local {
		return opts
	}
	out := []addon.Option{}
	for _, o := range opts {
		if h, _, err := net.SplitHostPort(o.Value); (err == nil && self[h]) || self[o.Value] {
			continue
		}
		out = append(out, o)
	}
	return out
}

// scanClient makes one request per connection with the scan's timeouts.
var scanClient = &http.Client{Transport: &http.Transport{
	DialContext: scanDialer.DialContext, DisableKeepAlives: true, ResponseHeaderTimeout: scanHostTimeout,
}}

// findProclaim probes each host:port and lists the Proclaims.
func findProclaim(ctx context.Context, targets []string) []addon.Option {
	return scan(ctx, targets, probeProclaim)
}

func probeProclaim(ctx context.Context, addr string) (addon.Option, bool) {
	code, body, err := scanGet(ctx, "http://"+addr+"/")
	if err != nil || code != 200 || !strings.Contains(body, helloProbe) {
		return addon.Option{}, false
	}
	host, port, _ := net.SplitHostPort(addr)
	onAir := "?"
	if code, _, err := scanGet(ctx, "http://"+addr+"/onair/session"); err == nil {
		switch code {
		case 200:
			onAir = "yes"
		case 204:
			onAir = "no"
		}
	}
	value := host
	if port != "52195" {
		value = addr
	}
	return addon.Option{Value: value, Label: value + " — Proclaim on " + machineName(ctx, host) + " (on air: " + onAir + ")"}, true
}

// scanGet is one short GET.
func scanGet(ctx context.Context, u string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := scanClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(b), nil
}

// machineName is the host's reverse-DNS name when it comes back fast,
// else the address itself.
func machineName(ctx context.Context, host string) string {
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return "this computer"
	}
	lctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(lctx, host)
	if err != nil || len(names) == 0 {
		return host
	}
	n := strings.TrimSuffix(names[0], ".")
	if n == "" || n == "localhost" {
		return host
	}
	return n
}
