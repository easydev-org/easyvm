// EGRESS RULES, WHICH NOW LIVE IN github.com/easydev-org/easytun/egress.
//
// THIS FILE IS THE PART THAT IS THIS DAEMON'S: the environment variables that seed the upstream table,
// the names the rule engine types are aliased under, and the wiring between the engine and the
// resolver. The matching, the CRUD and the resolution are the library's, because none of it needs to
// know what a guest is - see that package for the rules of the rule language.
package net

import (
	"net"
	"time"

	"github.com/easydev-org/easytun/egress"
)

// THE TYPE ALIASES KEEP EVERY EXISTING CALLER COMPILING. `net.Verdict` and `egress.Verdict` are the
// same type, so a caller cannot tell the difference and a future move of this package does not churn
// the daemon.
type (
	// Verdict is what happens to one connection, and through which upstream.
	Verdict = egress.Verdict
	// Kind is the three things that can happen to a connection.
	Kind = egress.Kind
	// Rule is one line of the table.
	Rule = egress.Rule
	// Policy is the whole configuration.
	Policy = egress.Policy
	// Upstream is one proxy traffic may be sent through.
	Upstream = egress.Upstream
)

// THE KIND CONSTANTS, re-exported so `net.KindProxy` reads the same as it always did.
const (
	KindProxy  = egress.Proxy
	KindDirect = egress.Direct
	KindBlock  = egress.Block
)

// EgressHooks wires the engine to this daemon's resolver, once, before anything asks a question.
//
// THE ENGINE DOES NOT IMPORT THE RESOLVER, and that is what lets it be a library: it asks "what names
// does this address have" through a function the application sets. Here the answer comes from the
// resolver in dns.go, which is the same table the DNS redirect fills.
func init() {
	egress.NamesFor = NamesFor
}

// DefaultUpstreams builds the table from the two variables this daemon has always had.
//
// THE DEFAULT NAME IS `default`, and it is what an unnamed verdict means. That keeps every rule
// written before this feature working unchanged.
func DefaultUpstreams() []Upstream {
	http := osGetenv(proxyEnv)
	socks := osGetenv(udpRelayUpstreamEnv)
	if http == "" && socks == "" {
		return nil
	}
	return []Upstream{{Name: "default", HTTP: http, SOCKS5: socks}}
}

// THE OLD NAMES FORWARD, so the rest of this package reads as it did. Each is a one-line wrapper
// around the library, which is the point: the code that used these names did not change, only where
// the behaviour lives.

// VerdictFor answers what to do with a connection to ip:port.
func VerdictFor(ip net.IP, port int) Verdict { return egress.For(ip, port) }

// SetEgress installs a policy and resolves it.
func SetEgress(p Policy) error { return egress.SetEgress(p) }

// Egress returns the policy in force, with the resolution outcome for each name.
func Egress() (Policy, []string, time.Time) { return egress.Egress() }

// EgressRules returns the rules in force, each with its derived ID.
func EgressRules() []Rule { return egress.Rules() }

// AddRule appends one rule and resolves the table again.
func AddRule(r Rule) (Rule, error) { return egress.AddRule(r) }

// ReplaceRule swaps the rule with this ID for a new one.
func ReplaceRule(id string, r Rule) (Rule, error) { return egress.ReplaceRule(id, r) }

// DeleteRule removes the rule with this ID and reports whether it existed.
func DeleteRule(id string) (bool, error) { return egress.DeleteRule(id) }

// SetDefault changes what an unmatched destination gets, leaving the rules alone.
func SetDefault(v Verdict) error { return egress.SetDefault(v) }

// DefaultVerdict is the current default, for the API to report.
func DefaultVerdict() Verdict { return egress.Default() }

// SetUpstreams installs a table from the rules API.
func SetUpstreams(list []Upstream) error { return egress.SetUpstreams(list) }

// UpstreamsCopy returns the table, for the API to display.
func UpstreamsCopy() []Upstream { return egress.Upstreams() }

// httpFor returns the CONNECT proxy an upstream name refers to.
func httpFor(name string) (string, bool) { return egress.HTTPFor(name) }

// socksFor returns the SOCKS5 relay an upstream name refers to.
func socksFor(name string) (string, bool) { return egress.SOCKSFor(name) }

// RuleID derives the identifier from what the rule says.
func RuleID(r Rule) string { return egress.RuleID(r) }

// StartEgressRefresh re-resolves rule names on a timer until done is closed.
func StartEgressRefresh(done <-chan struct{}) { egress.StartRefresh(done) }

// EgressRulesEnabled reports whether the daemon was told to expose the rules API.
//
// IT IS OFF UNLESS ASKED FOR, because the rules are a policy a person builds by hand and an interface
// that appears unbidden invites somebody to cut their own guest off. `EASYVM_EGRESS_RULES=1`.
func EgressRulesEnabled() bool { return osGetenv("EASYVM_EGRESS_RULES") == "1" }
