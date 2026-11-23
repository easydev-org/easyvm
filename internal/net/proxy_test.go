package net

import (
	"testing"
)

// THESE ARE THE PARTS OF THE TRANSPARENT PROXY THAT ARE THIS DAEMON'S: that it starts only when asked,
// and that a malformed upstream is refused before anything listens. The TUNNEL itself - CONNECT, the
// refusal, the copy loop - is github.com/easydev-org/easytun's and has its own tests there.
//
// A GUEST THAT KNOWS NOTHING ABOUT PROXIES STILL HAS TO GO THROUGH ONE, which is why the redirection
// is in iptables rather than in an environment variable: `pkg`, `fetch`, `git` and anything a
// walkthrough downloads each decide for themselves whether to honour `http_proxy`, and a bake that
// works only for the tools that happened to read it measures those tools.
func TestNoProxyMeansNoListener(t *testing.T) {
	t.Setenv(proxyEnv, "")
	if up, ok := ProxyConfigured(); ok {
		t.Fatalf("ProxyConfigured() reported %q with %s unset", up, proxyEnv)
	}
}

func TestTheUpstreamIsTheEnvironmentAndNothingElse(t *testing.T) {
	const addr = "172.17.0.121:7890"
	t.Setenv(proxyEnv, addr)
	up, ok := ProxyConfigured()
	if !ok {
		t.Fatalf("ProxyConfigured() said there was no proxy with %s=%s", proxyEnv, addr)
	}
	if up != addr {
		t.Fatalf("ProxyConfigured() = %q, want %q", up, addr)
	}
}

// A MALFORMED UPSTREAM IS REFUSED AT STARTUP, NOT AT THE FIRST GUEST REQUEST.
//
// The difference matters: a daemon that accepted `mihomo` and failed later would leave every guest
// connection going nowhere, and the symptom would be a download that hangs inside `pkg` - three
// layers away from the typo.
func TestAMalformedUpstreamIsRefusedBeforeAnythingListens(t *testing.T) {
	for _, bad := range []string{"mihomo", "mihomo:proxy", ":7890"} {
		t.Run(bad, func(t *testing.T) {
			if p, err := StartTransparentProxy(bad); err == nil {
				p.Close()
				t.Fatalf("StartTransparentProxy(%q) succeeded; it should have been refused", bad)
			}
		})
	}
}
