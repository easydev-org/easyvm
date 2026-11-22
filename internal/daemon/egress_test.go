package daemon

// THE EGRESS API HAD NO HANDLER, so these tests are the first thing that ever called it. They assert
// the two properties a rules endpoint must have: it is opt-in, and what it accepts is what the data
// path will match.

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	easyvmnet "github.com/easydev-org/easyvm/internal/net"
)

// egressD calls the handler directly, since it needs no machine - the whole point of a policy
// endpoint is that it changes a table and touches nothing else.
func (d *Daemon) egressD(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	switch {
	case path == "/api/egress":
		d.handleEgress(rec, req)
	case path == "/api/egress/rules":
		d.handleEgressRules(rec, req)
	default:
		d.handleEgressRule(rec, req)
	}
	return rec
}

// OFF BY DEFAULT, AND THE REFUSAL NAMES THE VARIABLE. A 404 would read as "no such endpoint", which
// would send somebody looking for a bug instead of a setting.
func TestTheEgressAPIIsOffUnlessEnabled(t *testing.T) {
	t.Setenv("EASYVM_EGRESS_RULES", "")
	d := &Daemon{}
	rec := d.egressD(http.MethodGet, "/api/egress", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "EASYVM_EGRESS_RULES") {
		t.Fatalf("the refusal does not name the variable: %q", rec.Body.String())
	}
}

// A TABLE GOES IN AND COMES BACK, AND THE DOMAIN RULE THEN MATCHES A CONNECTION.
//
// The last assertion is the point: this endpoint exists so a person can say "youtube.com goes through
// fast", and a handler that stored the rule without the matcher seeing it would pass every other
// check here.
func TestAPolicyGoesInAndTheMatcherUsesIt(t *testing.T) {
	t.Setenv("EASYVM_EGRESS_RULES", "1")
	t.Cleanup(func() {
		old := easyvmnet.DefaultUpstreams()
		_ = easyvmnet.SetUpstreams(old)
		_ = easyvmnet.SetEgress(easyvmnet.Policy{Default: easyvmnet.Verdict{Kind: easyvmnet.KindProxy}})
	})

	d := &Daemon{}
	body := `{
	  "default": "block",
	  "upstreams": [
	    {"name": "default", "http": "d:1", "socks5": "d:2"},
	    {"name": "fast", "http": "fast:1", "socks5": "fast:2"}
	  ],
	  "rules": [
	    {"match": "203.0.113.10", "action": "proxy:fast"},
	    {"match": "203.0.113.30", "action": "direct"}
	  ]
	}`
	rec := d.egressD(http.MethodPost, "/api/egress", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// THE RULE IS LIVE. An address rule needs no DNS, so this is the matcher straight from the endpoint.
	if v := easyvmnet.VerdictFor(parseIP("203.0.113.10"), 443); v.Upstream != "fast" {
		t.Fatalf("203.0.113.10 got %+v, want proxy:fast", v)
	}
	if v := easyvmnet.VerdictFor(parseIP("203.0.113.30"), 80); v.Kind != easyvmnet.KindDirect {
		t.Fatalf("203.0.113.30 got %+v, want direct", v)
	}
	// AND THE DEFAULT IS THE ONE THE REQUEST NAMED, so an unmatched destination is blocked rather than
	// silently proxied.
	if v := easyvmnet.VerdictFor(parseIP("198.51.100.1"), 443); v.Kind != easyvmnet.KindBlock {
		t.Fatalf("an unmatched address got %+v, want block", v)
	}

	// AND GET REPORTS IT BACK in the shape a caller can edit.
	rec = d.egressD(http.MethodGet, "/api/egress", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	var got struct {
		Default string           `json:"default"`
		Rules   []easyvmnet.Rule `json:"rules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET body is not JSON: %v", err)
	}
	if got.Default != "block" {
		t.Errorf("default = %q, want block", got.Default)
	}
	if len(got.Rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(got.Rules))
	}
	// EVERY RULE HAS AN ID, which is what the per-rule routes address.
	for _, r := range got.Rules {
		if r.ID == "" {
			t.Errorf("a rule came back without an id: %+v", r)
		}
	}
}

// A MALFORMED ACTION IS REFUSED RATHER THAN FOLDED INTO THE DEFAULT, because a typo that silently
// becomes `block` (or `proxy`) is a rule that does something other than what it says.
func TestABadActionIsRejected(t *testing.T) {
	t.Setenv("EASYVM_EGRESS_RULES", "1")
	d := &Daemon{}
	for _, action := range []string{"proxxy", "proxy:", "allow", ""} {
		rec := d.egressD(http.MethodPost, "/api/egress",
			`{"default":"proxy","rules":[{"match":"example.com","action":"`+action+`"}]}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("action %q: status = %d, want 400 (%s)", action, rec.Code, rec.Body.String())
		}
	}
}

// A RULE WITH NO MATCH IS REFUSED: an empty match would match nothing and look like a rule that is
// configured, which is the failure mode this endpoint exists to prevent.
func TestARuleNeedsAMatch(t *testing.T) {
	t.Setenv("EASYVM_EGRESS_RULES", "1")
	d := &Daemon{}
	rec := d.egressD(http.MethodPost, "/api/egress",
		`{"default":"proxy","rules":[{"match":"  ","action":"direct"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// APPEND, REPLACE AND DELETE, WHICH IS WHAT A UI EDITS ONE RULE WITH.
func TestOneRuleCanBeAddedReplacedAndDeleted(t *testing.T) {
	t.Setenv("EASYVM_EGRESS_RULES", "1")
	t.Cleanup(func() {
		_ = easyvmnet.SetUpstreams(easyvmnet.DefaultUpstreams())
		_ = easyvmnet.SetEgress(easyvmnet.Policy{Default: easyvmnet.Verdict{Kind: easyvmnet.KindProxy}})
	})
	d := &Daemon{}
	if rec := d.egressD(http.MethodPost, "/api/egress",
		`{"default":"proxy","upstreams":[{"name":"default","http":"d:1","socks5":"d:2"}],"rules":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}

	rec := d.egressD(http.MethodPost, "/api/egress/rules",
		`{"match":"203.0.113.44","action":"direct"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	var added easyvmnet.Rule
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	if added.ID == "" {
		t.Fatal("the added rule has no id")
	}

	// A DUPLICATE IS A CONFLICT, not a second identical rule.
	if rec := d.egressD(http.MethodPost, "/api/egress/rules",
		`{"match":"203.0.113.44","action":"direct"}`); rec.Code != http.StatusConflict {
		t.Errorf("duplicate: status = %d, want 409", rec.Code)
	}

	// REPLACE, AND THE ID CHANGES WITH THE CONTENT.
	rec = d.egressD(http.MethodPut, "/api/egress/rules/"+added.ID,
		`{"match":"203.0.113.44","action":"block"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body.String())
	}
	var updated easyvmnet.Rule
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.ID == added.ID {
		t.Error("the id did not change with the content, so a client would address the wrong rule")
	}
	if v := easyvmnet.VerdictFor(parseIP("203.0.113.44"), 443); v.Kind != easyvmnet.KindBlock {
		t.Errorf("after replace, the matcher says %+v, want block", v)
	}

	// DELETE BY THE NEW ID.
	if rec := d.egressD(http.MethodDelete, "/api/egress/rules/"+updated.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if v := easyvmnet.VerdictFor(parseIP("203.0.113.44"), 443); v.Kind != easyvmnet.KindProxy {
		t.Errorf("after delete, the matcher says %+v, want the default proxy", v)
	}
	// AND DELETING IT AGAIN IS A 404, not a silent success.
	if rec := d.egressD(http.MethodDelete, "/api/egress/rules/"+updated.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("second delete: status = %d, want 404", rec.Code)
	}
}

func parseIP(s string) net.IP { return net.ParseIP(s) }
