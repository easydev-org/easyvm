package daemon

import (
	"strings"
	"testing"
)

// THE SHARE CONFIG'S THREE LOAD-BEARING LINES, EACH WITH A FAILURE BEHIND IT.
//
// `share_serve:` was `not implemented` in this build, so every `vm-configure` stage that wanted a
// share failed at that step - thirteen of them, and the ones that reached it reported
//
//	not implemented in this build: step "serve the host share" needs "share_serve"
//
// These are the lines a Samba that "started perfectly and bound nothing" is missing.
func TestTheShareConfigCarriesTheLinesThatMatter(t *testing.T) {
	conf := shareConfig("/work/shared")

	// IT BINDS THE BRIDGE ADDRESS AND NOTHING ELSE. The guest reaches 172.30.0.1 over the tap; an
	// smbd listening on every interface is one the guest cannot necessarily route to, and the Python
	// implementation lost a macOS ssh stage to an smbd that logged "waiting for connections" against
	// an address that did not exist yet.
	for _, want := range []string{"interfaces = 172.30.0.1/24", "bind interfaces only = yes"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("the share config is missing %q:\n%s", want, conf)
		}
	}

	// THE GUEST ACCOUNT HAS NO PASSWORD, AND SAMBA REFUSES AN EMPTY ONE WITHOUT THIS. The failure it
	// prevents is `NT_STATUS_LOGON_FAILURE`, which reads as a wrong password.
	if !strings.Contains(conf, "null passwords = yes") {
		t.Fatalf("`null passwords = yes` is missing, so the guest's passwordless account is refused "+
			"with NT_STATUS_LOGON_FAILURE:\n%s", conf)
	}

	// AND THE GUEST NAME IS NOT A HOST ACCOUNT, so it must be mapped rather than rejected.
	if !strings.Contains(conf, "map to guest = bad user") {
		t.Fatalf("`map to guest = bad user` is missing, so Samba rejects `guest` before consulting "+
			"the share:\n%s", conf)
	}

	// THE SHARE ITSELF IS WRITABLE AND ANONYMOUS.
	for _, want := range []string{"path = /work/shared", "read only = no", "guest ok = yes"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("the share stanza is missing %q:\n%s", want, conf)
		}
	}
}

// AND A SECOND `share_serve:` FOR A DIFFERENT DIRECTORY IS REFUSED BY NAME.
//
// ONE GUEST HAS ONE SHARE: a second directory would need a second server, a second port or a second
// name, and none of those is in the file. A silent second server is worse than a refusal, because
// `share_mount:` would mount whichever won the race.
func TestASecondShareDirectoryIsRefused(t *testing.T) {
	d := &Driver{work: t.TempDir(), pass: "admin"}
	// Pretend one is already being served, without starting smbd.
	d.share = &shareServer{dir: "/work/first"}
	err := d.ServeShare("/work/second")
	if err == nil {
		t.Fatal("a second share directory must be refused; one guest has one share")
	}
	if !strings.Contains(err.Error(), "/work/first") {
		t.Fatalf("the refusal must name the directory already being served; got %v", err)
	}
	// AND THE SAME DIRECTORY TWICE IS NOT AN ERROR, which is what a stage that restarts needs.
	if err := d.ServeShare("/work/first"); err != nil {
		t.Fatalf("serving the same directory twice must be a no-op: %v", err)
	}
}
