package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNativeControlRetiresOnlyConfirmedResultsAfterTheirBindingCannotBeUsed(t *testing.T) {
	now := time.Now()
	finished := make(chan struct{})
	close(finished)
	entry := nativeControllerEntry{mutations: map[string]*nativeControlMutation{
		"accepted-expired":  {done: finished, retainUntil: now.Add(-time.Second)},
		"rejected-expired":  {done: finished, retainUntil: now.Add(-time.Second), err: ErrNativeControlStale},
		"accepted-current":  {done: finished, retainUntil: now.Add(time.Minute)},
		"uncertain-expired": {done: finished, retainUntil: now.Add(-time.Hour), err: ErrNativeControlUncertain},
		"still-in-flight":   {done: make(chan struct{}), retainUntil: now.Add(-time.Hour)},
	}}
	entry.pruneNativeMutations(now)
	assert.NotContains(t, entry.mutations, "accepted-expired")
	assert.NotContains(t, entry.mutations, "rejected-expired")
	assert.Contains(t, entry.mutations, "accepted-current")
	assert.Contains(t, entry.mutations, "uncertain-expired")
	assert.Contains(t, entry.mutations, "still-in-flight")
}
