package run

// `input text "x"` MUST TYPE "x". The dispatcher accepted both names and the typing read one.
//
// `doInput` routes a step carrying `text:` OR `type:` to `doType`, and `doType` read only
// `s.Get("type")`. So every 2.0 `input text "..."` compiled, passed `easyvm check`, dispatched
// correctly - AND TYPED NOTHING, because the value it wanted was under `text:`:
//
//	=== create your account - full name - type the value
//	  typed 0 character(s)
//
// MEASURED on ubuntu install, run 45089339918: the account form was never filled, Continue did
// nothing, the walk re-photographed "your account" twenty-four times and died at step 52 of 59.
// The click, the ctrl+a and the backspace all worked, which is what made it look like a UI fault.
//
// 2.0 SPELLS IT `text:`; 1.0 SPELLED IT `type:`. Both reach this function, so both must type.

import (
	"testing"

	"github.com/easydev-org/easyvm/internal/walk"
)

func TestInputTextTypesTheValueUnderText(t *testing.T) {
	d := &fakeDriver{}
	ex, steps := newExec(d, map[string]any{
		"name": "type the value", "input": true, "text": "VM User",
	})
	if _, err := ex.Run(steps); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(d.typed) != 1 || d.typed[0] != "VM User" {
		t.Fatalf("typed %q; `input text \"VM User\"` must type that string. A walk whose typing "+
			"silently types nothing leaves every form empty and fails much later, on a button that "+
			"did not advance", d.typed)
	}
}

// AND THE 1.0 NAME STILL WORKS, because files are being converted and both spellings reach here.
func TestInputTypeStillTypesTheValue(t *testing.T) {
	d := &fakeDriver{}
	ex, steps := newExec(d, map[string]any{
		"name": "type it the old way", "input": true, "type": "admin",
	})
	if _, err := ex.Run(steps); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(d.typed) != 1 || d.typed[0] != "admin" {
		t.Fatalf("typed %q; the 1.0 spelling of the same field must keep working", d.typed)
	}
}

var _ = walk.Step{}
