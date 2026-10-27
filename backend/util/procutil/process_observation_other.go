//go:build !windows

package procutil

import "context"

func platformObservationGone(context.Context, int, error) bool { return false }
