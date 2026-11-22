package daemon

// THE EGRESS RULES API: THE THING THAT MAKES "MANUALLY SET RULES" POSSIBLE.
//
// EVERYTHING BEHIND THIS FILE ALREADY EXISTED AND WAS UNREACHABLE. `internal/net` has the policy, the
// verdicts, the name resolution and the rule CRUD (`AddRule`, `ReplaceRule`, `DeleteRule`,
// `SetDefault`), and `EgressRulesEnabled` was written to gate a handler that was never wired. The
// result was that a person could not set a rule at all: TCP and UDP both consulted a table nothing
// could write to.
//
// THE SHAPE IS THE ONE THE DATA PATH READS, NOT A SECOND MODEL. A request carries the same fields
// `VerdictFor` uses - a match (`example.com`, `.example.com`, `*.ads.*`, `10.0.0.0/8`), an action
// (`proxy`, `proxy:NAME`, `direct`, `block`) and optional ports - so there is no translation layer to
// drift from the matcher. See internal/net/egress.go for what each shape means.
//
// IT IS OFF UNLESS EASYVM_EGRESS_RULES=1. A policy endpoint that appears unbidden is an endpoint that
// invites somebody to cut their own guest off; the switch is the deliberate opt-in the original code
// intended.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	easyvmnet "github.com/easydev-org/easyvm/internal/net"
)

// egressOff is the one line every route under /api/egress answers when the feature is not enabled.
//
// IT NAMES THE VARIABLE. A 404 would say "this daemon has no such endpoint", which is false - it has
// one and was told not to expose it - and the person reading it would look for a bug rather than a
// setting.
func (d *Daemon) egressOff(w http.ResponseWriter) bool {
	if !easyvmnet.EgressRulesEnabled() {
		http.Error(w, "the egress rules API is off; start the daemon with EASYVM_EGRESS_RULES=1",
			http.StatusForbidden)
		return true
	}
	return false
}

// egressAction parses the wire action into the Verdict the matcher uses.
//
//	proxy             the default upstream
//	proxy:NAME        a named upstream
//	direct            no proxy
//	block             refused
//
// THE PREFIX IS CHECKED BEFORE THE EQUALS so `proxy:fast` is not mistaken for a bare `proxy` with a
// stray suffix - the two mean different things and a typo must be a rejection, not the default.
func egressAction(s string) (easyvmnet.Verdict, error) {
	switch {
	case s == "proxy":
		return easyvmnet.Verdict{Kind: easyvmnet.KindProxy}, nil
	case s == "direct":
		return easyvmnet.Verdict{Kind: easyvmnet.KindDirect}, nil
	case s == "block":
		return easyvmnet.Verdict{Kind: easyvmnet.KindBlock}, nil
	case strings.HasPrefix(s, "proxy:"):
		name := strings.TrimPrefix(s, "proxy:")
		if name == "" {
			return easyvmnet.Verdict{}, fmt.Errorf("`proxy:` names no upstream")
		}
		return easyvmnet.Verdict{Kind: easyvmnet.KindProxy, Upstream: name}, nil
	}
	return easyvmnet.Verdict{}, fmt.Errorf("action %q is not proxy, proxy:NAME, direct or block", s)
}

// wireRule is one rule in a request, before its Verdict is parsed.
type wireRule struct {
	Match  string `json:"match"`
	Action string `json:"action"`
	Ports  []int  `json:"ports,omitempty"`
}

// toRule converts one wire rule, keeping the derived ID out of the request: the ID is a hash of the
// content and a caller cannot supply one that disagrees with what it describes.
func (r wireRule) toRule() (easyvmnet.Rule, error) {
	if strings.TrimSpace(r.Match) == "" {
		return easyvmnet.Rule{}, fmt.Errorf("a rule has no `match`")
	}
	v, err := egressAction(r.Action)
	if err != nil {
		return easyvmnet.Rule{}, fmt.Errorf("%s: %w", r.Match, err)
	}
	return easyvmnet.Rule{Match: r.Match, Action: r.Action, Verdict: v, Ports: r.Ports}, nil
}

// wirePolicy is the whole table as a caller sends it, for POST /api/egress.
type wirePolicy struct {
	Default   string     `json:"default"`
	Upstreams []wireUp   `json:"upstreams,omitempty"`
	Rules     []wireRule `json:"rules"`
}

type wireUp struct {
	Name   string `json:"name"`
	HTTP   string `json:"http,omitempty"`
	SOCKS5 string `json:"socks5,omitempty"`
}

// handleEgress serves GET (the policy in force) and PUT/POST (replace it).
//
// PUT AND POST DO THE SAME THING ON PURPOSE: this is a whole-table replace, and a client that POSTs
// one probably means "install this table" rather than "append one rule" - the per-rule routes below
// are where appending lives, and making POST partially apply would be the surprising choice.
func (d *Daemon) handleEgress(w http.ResponseWriter, r *http.Request) {
	if d.egressOff(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		policy, unresolved, at := easyvmnet.Egress()
		writeJSON(w, http.StatusOK, map[string]any{
			"default":     policy.Default.String(),
			"rules":       easyvmnet.EgressRules(),
			"unresolved":  unresolved,
			"resolved_at": at,
			"upstreams":   easyvmnet.UpstreamsCopy(),
		})
	case http.MethodPut, http.MethodPost:
		d.replaceEgress(w, r)
	default:
		http.Error(w, "GET, PUT or POST", http.StatusMethodNotAllowed)
	}
}

func (d *Daemon) replaceEgress(w http.ResponseWriter, r *http.Request) {
	var req wirePolicy
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "the policy is not valid JSON for this endpoint: "+err.Error(), http.StatusBadRequest)
		return
	}

	// THE UPSTREAM TABLE FIRST, BECAUSE A RULE THAT NAMES ONE IS VALIDATED AGAINST IT. Setting the
	// rules before the upstreams they refer to would reject a policy that is correct as a whole.
	if len(req.Upstreams) > 0 {
		list := make([]easyvmnet.Upstream, 0, len(req.Upstreams))
		for _, u := range req.Upstreams {
			if strings.TrimSpace(u.Name) == "" {
				http.Error(w, "an upstream has no `name`", http.StatusBadRequest)
				return
			}
			list = append(list, easyvmnet.Upstream{Name: u.Name, HTTP: u.HTTP, SOCKS5: u.SOCKS5})
		}
		if err := easyvmnet.SetUpstreams(list); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	def := easyvmnet.Verdict{Kind: easyvmnet.KindProxy}
	if req.Default != "" {
		v, err := egressAction(req.Default)
		if err != nil {
			http.Error(w, "default: "+err.Error(), http.StatusBadRequest)
			return
		}
		def = v
	}

	rules := make([]easyvmnet.Rule, 0, len(req.Rules))
	for _, wr := range req.Rules {
		rule, err := wr.toRule()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rules = append(rules, rule)
	}

	if err := easyvmnet.SetEgress(easyvmnet.Policy{Default: def, Rules: rules}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// THE RESPONSE IS THE RESOLVED TABLE, not the request echoed back. A name that did not resolve is
	// in `unresolved`, and that is the difference between "the rule was accepted" and "the rule will
	// match something" - a caller that only saw its own JSON would not know.
	policy, unresolved, at := easyvmnet.Egress()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"default":     policy.Default.String(),
		"rules":       easyvmnet.EgressRules(),
		"unresolved":  unresolved,
		"resolved_at": at,
	})
}

// handleEgressRules serves GET (list) and POST (append one rule) for /api/egress/rules.
func (d *Daemon) handleEgressRules(w http.ResponseWriter, r *http.Request) {
	if d.egressOff(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"rules": easyvmnet.EgressRules()})
	case http.MethodPost:
		var wr wireRule
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&wr); err != nil {
			http.Error(w, "a rule is a JSON object with match and action: "+err.Error(),
				http.StatusBadRequest)
			return
		}
		rule, err := wr.toRule()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		added, err := easyvmnet.AddRule(rule)
		if err != nil {
			// A DUPLICATE IS A 409, because the caller's rule is not malformed - it is already there,
			// and the ID in the body is what addresses it.
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusCreated, added)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// handleEgressRule serves PUT (replace) and DELETE for /api/egress/rules/{id}.
//
// THE ID COMES FROM THE PATH, so this handler trims the prefix rather than parsing a body that could
// disagree with the URL.
func (d *Daemon) handleEgressRule(w http.ResponseWriter, r *http.Request) {
	if d.egressOff(w) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/egress/rules/")
	if id == "" {
		http.Error(w, "a rule id is required in the path", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPut:
		var wr wireRule
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&wr); err != nil {
			http.Error(w, "a rule is a JSON object with match and action: "+err.Error(),
				http.StatusBadRequest)
			return
		}
		rule, err := wr.toRule()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		updated, err := easyvmnet.ReplaceRule(id, rule)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		// THE NEW ID IS IN THE BODY, because a content hash changes when the content does - a client
		// that cached the old one must read this.
		writeJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		found, err := easyvmnet.DeleteRule(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !found {
			http.Error(w, fmt.Sprintf("no rule with id %q", id), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "PUT or DELETE", http.StatusMethodNotAllowed)
	}
}
