package net

// THE POLICY AND UPSTREAM TABLES ARE PROCESS-WIDE, so a test that replaces them can corrupt a test
// running beside it. That is what produced a failure elsewhere in this repository while this work was
// being written: two tests set different upstream tables and the second saw the first's.
//
// THE FIX IS A LOCK RATHER THAN `t.Parallel()` BOOKKEEPING. There is nothing to parallelise about a
// global table - the tests that need it are short and the lock costs nothing. Every test that calls
// SetEgress or SetUpstreams takes this first, and it restores both tables when it is done.

import (
	"sync"
	"testing"
)

var policyMu sync.Mutex

// lockPolicy serialises a test that mutates the policy or upstream tables.
//
// THE NAME TABLE IS THE LIBRARY'S NOW, and it has its own reset in `names`. A test here that records a
// name does so through that package and cannot reach into another test's state by accident, because
// the table is keyed by address and every test uses its own.
func lockPolicy(t *testing.T) {
	t.Helper()
	policyMu.Lock()
	oldUp := UpstreamsCopy()
	oldPolicy, _, _ := Egress()
	t.Cleanup(func() {
		_ = SetUpstreams(oldUp)
		_ = SetEgress(oldPolicy)
		policyMu.Unlock()
	})
}
