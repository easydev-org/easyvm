package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// EVERY TOOL THE BUILD ASSERTS MUST BE IN AN INSTALL LINE.
//
// SWTPM WAS NOT, and the build found it:
//
//	#10 0.114 + swtpm --version
//	#10 0.114 /bin/sh: 1: swtpm: not found
//	ERROR: ... did not complete successfully: exit code: 127
//
// It had been described in the comment at the top of the file and asserted at the bottom, which is
// exactly the pair that makes a reader believe it is installed. The rewrite had dropped it from the
// apt list. Asserting a tool is not installing it, and installing it is not asserting it - both have
// to be true, and nothing was comparing the two lists.
func TestAssertedToolsAreInstalled(t *testing.T) {
	installs, asserts := splitInstallAndAssert(t)

	// THE COMMANDS THE ASSERTION BLOCK RUNS must each come from a package that is installed.
	// A handful of commands are provided by the base image or by a dependency and are listed here
	// rather than guessed at.
	from := map[string]string{
		"qemu-system-x86": "qemu-system-x86",
		"qemu-img":        "qemu-utils",
		"ovmf":            "ovmf",
		"swtpm":           "swtpm",
		// `smbd` IS IN `samba`, NOT `samba-common-bin`. This map said the latter and the test passed
		// while a built image could not run `smbd` at all:
		//
		//	serve the host share: smbd is not installed: exec: "smbd": executable file not found
		//
		// THE TEST COMPARED TWO LISTS THAT WERE BOTH WRONG IN THE SAME WAY, which is what a map from
		// tool to package is when nobody has built the image and run the tool.
		"smbd":          "samba",
		"dnsmasq":       "dnsmasq",
		"ip":            "iproute2",
		"iptables":      "iptables",
		"sysctl":        "procps",
		"ffmpeg":        "ffmpeg",
		"tesseract":     "tesseract-ocr",
		"debugfs":       "e2fsprogs",
		"xorriso":       "xorriso",
		"genisoimage":   "genisoimage",
		"mformat":       "mtools",
		"7z":            "p7zip-full",
		"aria2c":        "aria2",
		"unzstd":        "zstd",
		"wimlib-imagex": "wimtools",
	}

	for tool, pkg := range from {
		if !strings.Contains(asserts, tool) {
			continue // not asserted; nothing to compare
		}
		if !strings.Contains(installs, pkg) {
			t.Errorf("the build asserts %q but package %q is not in any install line. "+
				"Asserting a tool does not install it - that is how swtpm was missed", tool, pkg)
		}
	}

}

// splitInstallAndAssert returns the text of every line that INSTALLS a package and every line that
// ASSERTS a binary, so the two lists can be compared.
//
// IT DOES NOT PARSE RUN BLOCKS. A line-continued RUN puts the command on one line and the package
// names on the next dozen, so "which packages does this file install" is answered by collecting
// every line that is not a comment and is not the assertion block - the apt lists are the only
// place a package name can legally appear.
func splitInstallAndAssert(t *testing.T) (installs, asserts string) {
	t.Helper()
	body, err := os.ReadFile("../../Containerfile")
	if err != nil {
		t.Fatal(err)
	}

	var install, assert strings.Builder
	inAssert := false
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		// PROSE IS NOT A PACKAGE LIST. The header explains every package at length, and a grep over
		// the whole file would find `swtpm` there and call it installed. That mistake has been made
		// twice already in this repository's checks.
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		// THE ASSERTION RUN STARTS at the first --version and runs to its closing backslash-less
		// line; everything before it is installation.
		if strings.Contains(line, "--version") || strings.Contains(line, "command -v") {
			inAssert = true
		}
		if inAssert {
			assert.WriteString(line)
			assert.WriteString("\n")
			continue
		}
		install.WriteString(line)
		install.WriteString("\n")
	}
	return install.String(), assert.String()
}

// THE IMAGE MUST START THE PROGRAM WHEN A CALLER NAMES A SUBCOMMAND.
//
// `ENTRYPOINT []` MADE EVERY CALLER SPELL A PATH INSIDE THE IMAGE. Docker treats the first word of
// the command as an executable when the entrypoint is empty, so
//
//	docker run ghcr.io/easydev-org/easyvm:latest serve --work /work
//
// failed with `exec: "serve": executable file not found in $PATH` and exit 127 (run 43014353957),
// and `easyvm pull` only worked because that step happened to pass --entrypoint explicitly. An
// image whose own subcommands cannot be typed is not usable, and the failure names a shell path
// rather than the image.
func TestTheImageEntrypointIsTheProgram(t *testing.T) {
	body, err := os.ReadFile("../../Containerfile")
	if err != nil {
		t.Skipf("no Containerfile here (%v)", err)
	}
	// COMMENT LINES ARE WHERE THE FILE RECORDS WHY THE EMPTY ENTRYPOINT WAS WRONG, so the check
	// reads INSTRUCTIONS - a line that starts with the word.
	var instructions []string
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		instructions = append(instructions, trimmed)
	}
	text := strings.Join(instructions, "\n")

	if strings.Contains(text, "ENTRYPOINT []") {
		t.Fatal("the image has an empty entrypoint, so `docker run IMAGE serve` looks for an " +
			"executable named `serve` and exits 127")
	}
	if !strings.Contains(text, `ENTRYPOINT ["/usr/local/bin/easyvm"]`) {
		t.Fatal("the entrypoint must be the easyvm binary itself, at the path the COPY above " +
			"writes - a caller should never have to spell a path inside the image")
	}
	// AND NO DEFAULT SUBCOMMAND. A container that starts a daemon when the caller asked for
	// `check` is worse than one that prints usage: the first looks like it worked.
	if strings.Contains(text, `CMD ["serve"]`) || strings.Contains(text, "CMD serve") {
		t.Fatal("the image must not have a default `serve`; a container with no arguments " +
			"should print usage rather than start a daemon")
	}
}

// THE PACKAGE THAT OWNS `smbd` IS `samba`, AND THE MAP SAID OTHERWISE FOR AS LONG AS NOTHING RAN IT.
//
// THIS IS THE FAILURE MODE THE MAP ITSELF HAS: it compares a list of tools against a list of packages,
// so when the MAP is what is wrong, both sides agree and the test passes. A built image then answered
//
//	serve the host share: smbd is not installed: exec: "smbd": executable file not found in $PATH
//
// `samba-common-bin` carries the shared libraries, `smbclient`, `smbpasswd` and the init scripts. The
// DAEMON is in `samba`.
func TestTheDaemonPackagesAreTheOnesThatCarryTheDaemons(t *testing.T) {
	body, err := os.ReadFile("../../Containerfile")
	if err != nil {
		t.Skipf("no Containerfile here (%v)", err)
	}

	// THE APT LIST, AS NAMES. A package line is indented, may be quoted, and may carry a version pin
	// (`"qemu-utils=${VERSION_QEMU}"`), so the name is the token before any `=`.
	installed := map[string]bool{}
	inApt := false
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, "apt-get") && strings.Contains(trimmed, "install") {
			inApt = true
			continue
		}
		if inApt {
			if !strings.HasSuffix(trimmed, "\\") {
				inApt = false
				continue
			}
			name := strings.Trim(strings.TrimSuffix(trimmed, "\\"), ` "`)
			if i := strings.IndexByte(name, '='); i > 0 {
				name = name[:i]
			}
			installed[name] = true
		}
	}
	if len(installed) == 0 {
		t.Fatal("no apt install list was found in the Containerfile; this test is looking for " +
			"the wrong shape and would pass on an empty image")
	}

	// THE PAIRS THAT ARE EASY TO GET WRONG, because the tool's name and the package's do not match
	// and a near-miss package exists.
	pairs := map[string]string{
		"smbd":      "samba",         // NOT samba-common-bin: that has the libraries and smbclient
		"qemu-img":  "qemu-utils",    // not qemu-system-x86
		"sysctl":    "procps",        //
		"tesseract": "tesseract-ocr", //
		"7z":        "p7zip-full",    //
		"mformat":   "mtools",        // not dosfstools
	}
	for tool, pkg := range pairs {
		if !installed[pkg] {
			t.Errorf("%s comes from %s, which is not in the apt list. Installed: %v", tool, pkg,
				sortedKeys(installed))
		}
	}
	// AND `samba-common-bin` ALONE IS NOT ENOUGH - it is the exact wrong answer this test exists for.
	if installed["samba-common-bin"] && !installed["samba"] {
		t.Fatal("`samba-common-bin` is installed without `samba`, so `smbd` is missing while " +
			"`smbclient` and the libraries are present - a container that looks configured and " +
			"cannot serve a share")
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// THE FOLLOW LOOP MUST END BEFORE THE RUNNER ENDS THE JOB, OR THE DIAGNOSIS IS NEVER PRINTED.
//
// IT WAS `timeout + 5`, which is FIVE ROUNDS PAST the job's own `timeout-minutes`. Every leg that
// overran was therefore killed by GitHub mid-sleep, and the three lines a person needs
//
//	=== THE WALKTHROUGH DID NOT FINISH IN TIME ===
//	<daemon.log: which step it was on, and the error it hit>
//
// NEVER APPEARED. Measured on macos-setup: 97 minutes in `follow it` with 150 for the job, so its
// conclusion was going to be an opaque cancellation rather than a reason.
//
// THE TWO NUMBERS ARE IN DIFFERENT FILES AND MUST AGREE: `matrix.timeout` sets `timeout-minutes` on
// the job and the loop derives its own limit from it. A limit at or above the job's own budget cannot
// be reached, so the loop is a no-op and every overrun is silent.
func TestTheFollowLoopEndsBeforeTheJobDoes(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/stage.yml")
	if err != nil {
		t.Skipf("no stage.yml here (%v)", err)
	}
	src := string(body)

	// THE LIMIT IS DERIVED, AND IT SUBTRACTS RATHER THAN ADDS.
	limit := regexp.MustCompile(`limit=\$\(\( *\$\{\{ *matrix\.timeout *\}\} *([+-]) *(\d+) *\)\)`).FindStringSubmatch(src)
	if limit == nil {
		t.Fatal("the follow loop does not derive its limit from `matrix.timeout`; if it uses a " +
			"fixed number it cannot track the per-leg budget")
	}
	if limit[1] != "-" {
		t.Fatalf("the loop's limit is `matrix.timeout %s %s`, so it runs PAST the job's own "+
			"`timeout-minutes` and the runner kills it before the diagnosis is printed", limit[1], limit[2])
	}

	// AND THE JOB'S TIMEOUT IS THE SAME VALUE, not a second number that drifted.
	if !strings.Contains(src, "timeout-minutes: ${{ matrix.timeout }}") {
		t.Fatal("the job's `timeout-minutes` is not `matrix.timeout`, so the two are unrelated " +
			"and the subtraction above guarantees nothing")
	}

	// AND THERE IS ENOUGH HEADROOM FOR WHAT COMES AFTER. `what it produced` lists the work
	// directory and the artifact upload pushes the disk, which is gigabytes - ten minutes is the
	// floor for that on a slow runner, and less than that would make the loop end after the upload
	// was already cut off.
	headroom, err := strconv.Atoi(limit[2])
	if err != nil {
		t.Fatalf("the loop's headroom is not a number: %q", limit[2])
	}
	if headroom < 5 || headroom > 20 {
		t.Fatalf("the loop leaves %d minute(s) for the listing and the artifact upload; measured "+
			"uploads of a 128 GiB disk image need more than 5 and there is no reason to give away "+
			"more than 20", headroom)
	}
}

// A STEP'S `timeout` IS TWO DIFFERENT THINGS, AND ONLY ONE OF THEM CAN BE SUMMED.
//
// MOST ARE HANG DETECTORS: 900 seconds for "a screen that should change", 300 for "a dialog that should
// close". A step that takes nine minutes inside a 900-second budget is normal, so summing forty of them
// describes a machine stuck on every step at once - which is the arithmetic I did first, reached
// "28 of 47 legs are impossible", and gave windows-dev 440 minutes.
//
// ONE KIND IS DIFFERENT, AND macos-setup IS THE ONLY PLACE IT MATTERS: `the copy runs until the Setup
// Assistant appears` is 7200 seconds because WRITING MACOS ONTO A 128 GiB DISK REALLY TAKES TWO HOURS.
// That is not a limit the step might exceed; it is the work. With the boot (15) and recovery-menu (15)
// waits, the erase report (5), the copy's return (5) and the shutdown (5), the floor is 165 minutes -
// and the job had 150, so the install was cut off however well every step behaved.
//
// SO THIS TEST CHECKS THE ONE LEG WHERE THE SUM IS MEANINGFUL: macos-setup's job timeout must exceed the
// sum of the waits that are WORK, identified by being longer than the hang-detection default.
func TestMacOSSetupHasTimeForTheInstallItWaitsFor(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/macos.yml")
	if err != nil {
		t.Skipf("no macos.yml here (%v)", err)
	}
	src := string(body)

	// THE JOB'S BUDGET, FROM THE FILE THAT DECLARES IT.
	leg := regexp.MustCompile(`"name": "macos-setup"[\s\S]*?"timeout": (\d+)`).FindStringSubmatch(src)
	if leg == nil {
		t.Fatal("no macos-setup leg in macos.yml")
	}
	job, err := strconv.Atoi(leg[1])
	if err != nil {
		t.Fatal(err)
	}

	// THE WORK, FROM THE WALKTHROUGH ITSELF. Every limit of ten minutes or more is a wait for something
	// real - the install, and the boot and recovery-menu gates - and they are what the job must fit.
	wf, err := os.ReadFile("../../macos/15/steps/setup.yaml")
	if err != nil {
		t.Skipf("no setup.yaml here (%v)", err)
	}
	var work int
	for _, m := range regexp.MustCompile(`(?m)^\s+timeout:\s*(\d+)\s*$`).FindAllStringSubmatch(string(wf), -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n >= 600 {
			work += n
		}
	}
	workMin := work / 60

	// AND THE LOOP LEAVES HEADROOM BENEATH THE JOB, so the reason is printed rather than cancelled.
	const headroom = 10
	if job-headroom <= workMin {
		t.Fatalf("macos-setup gets %d minutes and its waits for real work are %d, so with the %d minutes "+
			"`follow it` leaves for its own diagnosis the job is cut off mid-install and reports a "+
			"cancellation instead of a step name", job, workMin, headroom)
	}
}

// EVERY WORKFLOW THAT PRODUCES A SHARED ARTEFACT MUST DECLARE CONCURRENCY.
//
// `image.yml` HAD NONE, and it publishes `:latest` - so every push queued another full build and they
// ran one after another. Measured: SIX BUILDS QUEUED behind two running walkthroughs, the oldest waiting
// thirty-five minutes to build a commit four revisions stale. Five of those six produced an image nobody
// would ever pull.
//
// THE OS WORKFLOWS ALREADY HAD THIS, each with its own group so dispatching one cannot cancel a run of
// another - that lesson cost forty minutes of a Windows install. `image.yml` is the one that was missed,
// because it is not dispatched by hand and nothing in a feature change makes a person think about it.
//
// THE RULE: any workflow with a `push:` trigger or a shared published artefact needs a group, and a
// group that cancels. A queued build of superseded code is not information worth keeping.
func TestEveryWorkflowThatPublishesDeclaresConcurrency(t *testing.T) {
	files, err := filepath.Glob("../../.github/workflows/*.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Skip("no workflows here")
	}
	var checked int
	for _, path := range files {
		rel, _ := filepath.Rel(filepath.Join("..", ".."), path)
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(body)
		// THE REUSABLE MECHANISM (`stage.yml`) IS CALLED BY OTHERS AND MUST NOT DECLARE A WORKFLOW-LEVEL
		// GROUP: doing so makes it unparseable and the dispatch creates zero jobs.
		if strings.Contains(text, "workflow_call:") {
			if strings.Contains(text, "\nconcurrency:") {
				t.Errorf("%s is a `workflow_call` workflow and declares `concurrency:`, which makes it "+
					"unparseable - the dispatch creates zero jobs", rel)
			}
			continue
		}
		// A `push:` TRIGGER MEANS EVERY COMMIT STARTS IT. That is the case that queued six image builds.
		if !strings.Contains(text, "\npush:") && !strings.Contains(text, "branches: [main]") {
			continue
		}
		checked++
		if !strings.Contains(text, "\nconcurrency:") {
			t.Errorf("%s has a `push:` trigger and no `concurrency:`, so every commit queues another "+
				"full run that the next commit supersedes before it starts", rel)
			continue
		}
		if !strings.Contains(text, "cancel-in-progress: true") {
			t.Errorf("%s declares `concurrency:` without `cancel-in-progress: true`, so superseded "+
				"runs queue instead of being replaced", rel)
		}
	}
	if checked == 0 {
		t.Skip("no push-triggered workflows here")
	}
	t.Logf("checked %d push-triggered workflow(s)", checked)
}
