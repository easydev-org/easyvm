package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/easydev-org/easyvm/internal/protocol"
)

// A REFUSAL MUST NOT LOOK LIKE A SUCCESS, AND A BAD FIELD MUST NOT BE IGNORED.
//
// THESE ARE THE THREE SHAPES A SILENT FAILURE TOOK ON THE POINTER ROUTES, each measured against a
// running daemon:
//
//	POST /api/click {"xx": 100, "yy": 200}   the keys do not match, so both decoded to zero, the click
//	                                         landed at (0, 0) and the reply was {"ok":true}
//	POST /api/click {}                       the same path with no body at all
//	POST /api/click {"x":1,"y":2}            (no machine) answered 200 with {"ok":false} in the body,
//	                                         which `curl -f` cannot see
//
// THE FIRST TWO ARE NOW A 400 THAT NAMES THE FIELD, the third a 409, and this test is what keeps them
// that way. The alternative - reading the JSON body for "ok" - is exactly the habit that let the first
// two survive.
func TestPointerRoutesRefuseBadInputInsteadOfClickingTheCorner(t *testing.T) {
	_, _ = startDaemon(t)
	base := "http://" + daemonAddr(t)

	cases := []struct {
		name   string
		body   string
		status int
		// mustMention is the word the error has to contain, so a wrong message is caught too: an
		// error that says "bad request" for a misspelled key sends the reader nowhere.
		mustMention string
	}{
		{
			name:        "misspelled keys are refused, not silently zeroed",
			body:        `{"xx": 100, "yy": 200}`,
			status:      http.StatusBadRequest,
			mustMention: "x",
		},
		{
			name:        "an empty body names both missing coordinates",
			body:        `{}`,
			status:      http.StatusBadRequest,
			mustMention: "x",
		},
		{
			name:        "only y is named as the missing one",
			body:        `{"x": 10}`,
			status:      http.StatusBadRequest,
			mustMention: "y",
		},
		{
			name:        "only x is named as the missing one",
			body:        `{"y": 10}`,
			status:      http.StatusBadRequest,
			mustMention: "x",
		},
		{
			// WELL-FORMED, AND REFUSED BY THE MACHINE: there is no guest to click.
			name:        "no machine is a conflict, not a 200",
			body:        `{"x": 10, "y": 20}`,
			status:      http.StatusConflict,
			mustMention: "",
		},
		{
			name:        "two JSON objects in one body is refused",
			body:        `{"x":1,"y":2}{"x":3,"y":4}`,
			status:      http.StatusBadRequest,
			mustMention: "one json object",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post(base+"/api/click", "application/json",
				bytes.NewReader([]byte(tc.body)))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.status {
				t.Fatalf("status is %d, want %d. A refusal that answers 200 is invisible to every "+
					"caller that does not parse the body", resp.StatusCode, tc.status)
			}
			if tc.mustMention == "" {
				return
			}
			var body bytes.Buffer
			_, _ = body.ReadFrom(resp.Body)
			got := strings.ToLower(body.String())
			if !strings.Contains(got, tc.mustMention) {
				t.Fatalf("the message does not mention %q, so it does not say which field is "+
					"wrong:\n\t%s", tc.mustMention, got)
			}
			// AND IT MUST NOT CLAIM TO HAVE CLICKED. A 400 that also says `"ok":true` is worse than
			// the silent version, because two parts of the reply disagree.
			if strings.Contains(got, `"ok":true`) {
				t.Fatalf("a refused request answered `ok: true`:\n\t%s", got)
			}
		})
	}
}

// daemonAddr is the address `startDaemon` published through EASYVM_HTTP.
func daemonAddr(t *testing.T) string {
	t.Helper()
	// `startDaemon` sets EASYVM_HTTP for the client, and this reads the same value rather than
	// threading a second return value through every existing caller.
	v := os.Getenv("EASYVM_HTTP")
	if v == "" {
		t.Fatal("EASYVM_HTTP is empty; startDaemon sets it")
	}
	return strings.TrimPrefix(v, "http://")
}

// A COORDINATE OF ZERO IS A PLACE, AND ITS ABSENCE IS NOT THE SAME VALUE.
//
// THE STRUCT USED `int` WITH `omitempty`, so `(0, 0)` - the top-left pixel, which a walkthrough may
// name - was DROPPED from the wire and arrived as "the caller sent nothing". A pointer makes `&0` and
// `nil` different values, which is what lets one be a click and the other a 400.
func TestACoordinateOfZeroIsCarriedAndAMissingOneIsNil(t *testing.T) {
	zero := 0
	wire, err := json.Marshal(protocol.Request{Command: protocol.CommandClick, X: &zero, Y: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), `"x":0`) || !strings.Contains(string(wire), `"y":0`) {
		t.Fatalf("`x: 0` was omitted from %s - the daemon cannot tell the corner from nothing", wire)
	}

	// AND AN ABSENT ONE DECODES TO nil, not to zero.
	var req protocol.Request
	if err := json.Unmarshal([]byte(`{"command":"click"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.X != nil || req.Y != nil {
		t.Fatalf("an absent coordinate decoded to %v/%v, so it is indistinguishable from (0, 0)",
			req.X, req.Y)
	}
	if _, _, err := xyOf("click", req); err == nil {
		t.Fatal("a request with no x or y was accepted")
	}
}
