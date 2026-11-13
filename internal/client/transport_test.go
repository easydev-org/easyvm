package client

import (
	"os"
	"strings"
	"testing"
)

// THE TWO TRANSPORTS CARRY THE SAME REQUEST, AND THAT IS THE WHOLE POINT OF HAVING TWO.
//
// THE SOCKET IS WHAT A SUPERVISOR USES: a filesystem permission, unreachable from another container,
// which is the shape "only this user may drive this VM" wants. HTTP is what a BROWSER uses and what a
// person uses when the daemon is in another pod.
//
// A SECOND DISPATCHER WOULD BE A SECOND ANSWER to "what does `stop` mean" - so both transports hand
// the same `protocol.Request` to the same `handle`, and this test keeps the HTTP side from growing a
// translation layer.
func TestBothTransportsSendTheSameRequestType(t *testing.T) {
	// `DialHTTP` builds a client with no socket, and `Dial` builds one with no base - so the transport
	// is a field rather than a different type, and every command works on both.
	sock, err := DialHTTP(":8080")
	if err != nil {
		t.Fatalf("DialHTTP: %v", err)
	}
	if sock.base == "" {
		t.Fatal("DialHTTP did not record its endpoint")
	}
	if sock.http == nil {
		t.Fatal("DialHTTP did not build an http.Client")
	}

	// THE ADDRESS FORMS A PERSON TYPES. `:8080` is what a Service exposes, `host:8080` is what a
	// README says, and `http://host:8080` is what a browser shows after a redirect.
	for _, addr := range []string{":8080", "host:8080", "http://host:8080", "http://host:8080/"} {
		c, err := DialHTTP(addr)
		if err != nil {
			t.Fatalf("DialHTTP(%q): %v", addr, err)
		}
		if !strings.HasSuffix(c.base, "8080") {
			t.Errorf("DialHTTP(%q) -> base %q, which does not end in the port", addr, c.base)
		}
	}
}

// AND A COMMAND THAT NEEDS A ROUTE OF ITS OWN GETS ONE, WITHOUT LOSING THE OTHERS.
//
// `/api/click`, `/api/key`, `/api/type` and `/api/double` are the fast paths a page polls; everything
// else goes to `/api/command` with the whole Request. A route that forgot one command would make the
// CLI work locally and fail over HTTP, which is the worst kind of difference.
func TestEveryCommandHasAHTTPRoute(t *testing.T) {
	body, err := os.ReadFile("../../internal/daemon/http.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	// the daemon must serve the catch-all the client posts to
	if !strings.Contains(src, `"/api/command"`) {
		t.Error("the daemon has no `/api/command` route, so a command without a fast path of its " +
			"own - `run`, `create`, `stop`, `destroy` - cannot be sent over HTTP at all")
	}
	for _, route := range []string{"/api/click", "/api/double", "/api/key", "/api/type",
		"/api/state", "/screen.png"} {
		if !strings.Contains(src, `"`+route+`"`) {
			t.Errorf("the daemon does not serve %s", route)
		}
	}
}
