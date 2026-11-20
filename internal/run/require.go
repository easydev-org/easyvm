package run

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/easydev-org/easyvm/internal/walk"
)

// doRequire refuses the walk unless every file it names is present in the work directory.
//
// WHY A STEP AND NOT A COMMENT. Every medium a walkthrough boots from is now pulled by the workflow
// BEFORE `easyvm run` is called - the installer ISOs, the macOS recovery image, OpenCore's three
// firmware files, the Xcode .xip, the dev package tarballs. That moved an 18-minute download out of
// the middle of a stage (measured on run 43847000071, where it ate the whole `wait for sshd` budget
// and reported a machine that never answered), and it also moved the DIAGNOSIS: a file that is
// missing now fails at the first `boot:` with
//
//	boot: no the installer medium at /work/ubuntu.iso: stat: no such file or directory
//
// which names the boot step and not the pull. This step is the one that names the pull.
//
// IT IS THE FIRST STEP OF EVERY WALKTHROUGH, AND IT FAILS FAST. A stage that discovers a missing ISO
// after ten minutes of clicking a live desktop has spent ten minutes to learn something available in
// the first second.
//
// IT ASKS ONE QUESTION: IS THE FILE THERE. Not how big it is, not whether it looks like the right
// format - a size floor was tried and removed, because the only size a walk can honestly name is the
// exact one, and a floor below it answers a question nobody asked while adding a second way for this
// step to be wrong. A pull that wrote nothing leaves no file, and that is what this catches.
//
// THE KEYS ARE `require:` AND EVERY ENTRY IS A FILENAME, relative to the work directory or absolute.
func (e *Executor) doRequire(s walk.Step) error {
	raw, ok := s.Spec["needs"]
	if !ok {
		raw, ok = s.Spec["require"]
	}
	if !ok {
		return fail(s.Name, "`needs:` needs a list of files that must be on disk before this stage "+
			"runs")
	}
	items, ok := raw.([]any)
	if !ok {
		return fail(s.Name, "`needs:` must be a list of file names, not %T", raw)
	}
	if len(items) == 0 {
		return fail(s.Name, "`needs:` names no files, so it would pass by checking nothing")
	}

	var problems []string
	for _, item := range items {
		entry, _ := item.(string)
		entry = strings.TrimSpace(entry)
		if entry == "" {
			problems = append(problems, "an empty entry")
			continue
		}

		path := entry
		if !filepath.IsAbs(path) {
			path = filepath.Join(e.Work, path)
		}
		fi, err := os.Stat(path)
		if err != nil {
			problems = append(problems, entry+" is not in "+e.Work)
			continue
		}
		if fi.IsDir() {
			problems = append(problems, entry+" is a directory, not a file")
			continue
		}
		e.Driver.Log(fmt.Sprintf("  required: %s", entry))
	}

	if len(problems) > 0 {
		// THE SENTENCE NAMES WHAT TO DO RATHER THAN WHAT HAPPENED, because the answer is always the
		// same one: the workflow's `assets:` list is what puts these files here, and a leg that
		// reaches this message has an `assets:` entry missing or a pull that failed.
		return fail(s.Name, "%s. Every medium a walk boots from is pulled BEFORE this process starts "+
			"- a leg's `assets:` in its workflow is what declares them, and `pull-artifact.sh` is what "+
			"places them. A missing file here means that pull did not run or did not finish, and the "+
			"run was stopped at its first step for that reason rather than forty minutes in",
			strings.Join(problems, "; "))
	}
	return nil
}
