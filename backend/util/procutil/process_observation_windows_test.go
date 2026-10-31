//go:build windows

package procutil

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/windows"
)

func TestWindowsObservationGoneRequiresConfirmedAbsence(t *testing.T) {
	t.Parallel()
	assert.True(t, windowsObservationGone(windows.ERROR_INVALID_PARAMETER, func() (bool, error) { return false, nil }))
	assert.False(t, windowsObservationGone(windows.ERROR_INVALID_PARAMETER, func() (bool, error) { return true, nil }))
	assert.False(t, windowsObservationGone(windows.ERROR_INVALID_PARAMETER, func() (bool, error) { return false, errors.New("lookup failed") }))
	assert.False(t, windowsObservationGone(windows.ERROR_ACCESS_DENIED, func() (bool, error) {
		t.Fatal("an ownership refusal must not be treated as a vanished process")
		return false, nil
	}))
}
