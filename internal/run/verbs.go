package run

import (
	"github.com/easydev-org/easyvm/internal/walk"
)

// THE THREE VERBS THAT HAVE NO IMPLEMENTATION OF THEIR OWN.
//
// EACH IS AN ADAPTER, NOT A SECOND ACTION. `press` covers the pointer and the keyboard, `control`
// covers the messages to the machine, and `stop` covers stopping plus publishing - and every one of
// them resolves to code that was measured against a real guest. The adapter exists because the 2.0
// file writes one verb where the executor has several branches, and the branches are the measured
// part.

// doPress dispatches the one input verb to the shape it describes.
//
// THE FIELD IS THE DISCRIMINATOR, and there is exactly one of them per step:
//
//	press 960 628              a click
//	press [960, 628]           the same, as a list
//	press Return               a key
//	press {key: ...}           a key, as a block
//	press {text: ...}          typing, optionally with `at:` to click first
//	press {button: ...}        click the button found by its label
//	press {colour: ...}        click the largest blob of that colour
func (e *Executor) doPress(s walk.Step) error {
	// A COORDINATE IS THE COMMONEST SHAPE, and it is a bare pair: `press 960 628`.
	if s.Has("click") {
		return e.doClick(s)
	}
	if s.Has("text") || s.Has("type") {
		// `at:` IS THE POINT TO CLICK BEFORE TYPING, which `doType` reads as `click:`.
		if s.Has("at") && !s.Has("click") {
			spec := map[string]any{}
			for k, v := range s.Spec {
				spec[k] = v
			}
			spec["click"] = spec["at"]
			s = walk.Step{Index: s.Index, Name: s.Name, Spec: spec}
		}
		return e.doType(s)
	}
	if s.Has("key") {
		return e.doKey(s)
	}
	if s.Has("colour") {
		return e.doAtColor(s)
	}
	if s.Has("button") {
		return e.doAtButton(s)
	}
	return fail(s.Name, "`press:` needs a coordinate, `key:`, `text:`, `button:` or `colour:`")
}

// doControl dispatches the one machine verb to the message it carries.
//
//	control acpi reset        an ACPI request
//	control qga ping          the guest agent
//	control stopped           wait until the process has gone
func (e *Executor) doControl(s walk.Step) error {
	if s.Has("acpi") {
		return e.doACPI(s)
	}
	if s.Has("qga") {
		return e.doQGA(s)
	}
	if s.Has("wait_until") || s.Get("stopped") != "" {
		return e.doWaitUntil(s)
	}
	return fail(s.Name, "`control:` needs `acpi:`, `qga:` or `stopped`")
}

// doStop stops the machine and, if the step says so, publishes the disk.
//
// THE TWO ARE ONE STEP BECAUSE THE SECOND CANNOT PRECEDE THE FIRST: `compress:` reads the qcow2, and
// qemu holds a write lock on it until the process is gone. MEASURED on zorin-install, whose install
// was complete and whose publish failed with `qemu-img: Failed to get shared "write" lock`.
func (e *Executor) doStop(s walk.Step) error {
	// `stop X` CARRIES ITS METHOD IN THE VERB ITSELF, so that is read first; the parser's `method:`
	// form is the block shape. The shutdown code reads `shutdown:`, which is what this builds.
	m := s.Get("method")
	if m == "" {
		m = s.Get("stop")
	}
	if m != "" && m != "true" && !s.Has("shutdown") {
		spec := map[string]any{}
		for k, v := range s.Spec {
			spec[k] = v
		}
		spec["shutdown"] = m
		s = walk.Step{Index: s.Index, Name: s.Name, Spec: spec}
	}
	if s.Has("shutdown") {
		if err := e.doShutdown(s); err != nil {
			return err
		}
	}
	if p := s.Get("save"); p != "" && !s.Has("compress") {
		spec := map[string]any{}
		for k, v := range s.Spec {
			spec[k] = v
		}
		spec["compress"] = p
		s = walk.Step{Index: s.Index, Name: s.Name, Spec: spec}
	}
	if s.Has("compress") {
		return e.doCompress(s)
	}
	return nil
}
