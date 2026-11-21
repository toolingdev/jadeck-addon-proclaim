package main

// The connection test's one-liner: what's wrong and what to change.

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"time"
)

// checkPassword tries the network control login once for a Proclaim on
// another PC, so a wrong password shows up in the connection test rather
// than on the first button press.
func (c *Conn) checkPassword(ctx context.Context) error {
	if c.local {
		return nil
	}
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()
	if tok != "" {
		return nil
	}
	_, err := c.authenticate(ctx)
	return err
}

// explain turns a failed check into one line saying what to fix.
func (c *Conn) explain(err error) string {
	addr := strings.TrimPrefix(c.base, "http://")
	host, _, _ := net.SplitHostPort(addr)
	var dns *net.DNSError
	var ne net.Error
	switch {
	case strings.Contains(err.Error(), "network control"):
		return err.Error() // password problems already say what to do
	case errors.As(err, &dns):
		return "No computer called \"" + host + "\" was found — check the Proclaim PC's address."
	case errors.Is(err, syscall.ECONNREFUSED), strings.Contains(err.Error(), "connection refused"), strings.Contains(err.Error(), "actively refused"):
		return host + " is on, but Proclaim's remote isn't (port 52195 closed) — open Proclaim, then In Proclaim: Settings → Remote → Local Server → Enable."
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), strings.Contains(err.Error(), "connection reset"):
		return addr + " hung up straight away — a firewall or another program is in the way; check the address."
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH), strings.Contains(err.Error(), "no route to host"):
		return "Can't reach " + host + " — check the address and that the PC is on and on this network."
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "No answer from " + host + " — check the address, that the PC is on, and that its firewall lets in Proclaim (port 52195)."
	}
	// It answered, but not as expected: is it Proclaim at all?
	pctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if code, body, gerr := scanGet(pctx, c.base+"/"); gerr == nil && (code != 200 || !strings.Contains(body, helloProbe)) {
		return addr + " answers, but it isn't Proclaim's Local Server — check the address."
	}
	return err.Error()
}
