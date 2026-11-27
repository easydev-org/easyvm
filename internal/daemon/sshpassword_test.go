package daemon

import (
	"testing"

	"github.com/easydev-org/easyvm/internal/walk"
)

// THE FAILURE THIS PREVENTS: a stage that passes no `--password` gets the file's own `password:`
// back, not an empty string.
//
// MEASURED, run 45915469545 closed it: ten of twelve Linux ssh workflows passed no `--password`
// (stage.yml only forwards it when the caller sets an input), `easyvm run`'s flag defaulted to
// "admin", and every guest's account is "admin1" - so the probe and every `ssh:` step answered
// "unable to authenticate ... no supported methods remain" on a machine whose sshd was active,
// listening and reachable. The engine's own client accepted "admin1" and refused "admin" on the
// same guest in the lab pod.
func TestSSHPasswordFallsBackToTheFilesOwnValue(t *testing.T) {
	f := &walk.File{Vars: map[string]any{"password": "admin1"}}

	if got := sshPassword("", f); got != "admin1" {
		t.Errorf("no caller password: got %q, want the file's own %q", got, "admin1")
	}
	if got := sshPassword("override", f); got != "override" {
		t.Errorf("a caller password must win: got %q, want %q", got, "override")
	}
	if got := sshPassword("", &walk.File{}); got != "" {
		t.Errorf("a file with no password must not invent one: got %q", got)
	}
	if got := sshPassword("", nil); got != "" {
		t.Errorf("no file must not panic or invent a value: got %q", got)
	}
}
