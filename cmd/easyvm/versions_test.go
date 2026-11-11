package main

import (
	"os"
	"strings"
	"testing"
)

// `versions` NAMES ONLY COMMANDS THIS BUILD ACTUALLY RUNS.
//
// It listed `websockify` after the console replaced websockify, and that is a small lie with a real
// cost: a reader looking at a failed build sees "websockify (not on PATH)" and goes looking for a
// package the program does not want. The list is the host-side surface of the implementation, so it
// is asserted against the packages that hold that surface rather than against a comment.
func TestVersionsNamesOnlyWhatIsUsed(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	// websockify IS GONE, and the console's own file says why.
	if strings.Contains(src, `"websockify"`) {
		t.Fatal("`versions` still lists websockify; internal/daemon serves the console itself " +
			"and bridges the WebSocket, so there is no such dependency")
	}

	// AND THE FOUR THE NETWORK NEEDS ARE NAMED, because a missing one of them is the difference
	// between a guest on a bridge and `RTNETLINK answers: Operation not permitted`.
	for _, want := range []string{`"ip"`, `"iptables"`, `"dnsmasq"`, `"sysctl"`} {
		if !strings.Contains(src, want) {
			t.Fatalf("`versions` does not report %s; it is a host command the network layer runs "+
				"and the first thing to check when a guest cannot be reached", want)
		}
	}
	// AND THE MACHINE'S OWN TWO, which the whole program is about.
	for _, want := range []string{`"qemu-system-x86_64"`, `"qemu-img"`, `"smbd"`} {
		if !strings.Contains(src, want) {
			t.Fatalf("`versions` does not report %s", want)
		}
	}
}
