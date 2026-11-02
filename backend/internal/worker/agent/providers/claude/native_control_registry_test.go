package claude

import (
	"net/http"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/require"
)

func TestNativeControlEnrollmentRegistryBeforeInspectionAndAfterDisconnect(t *testing.T) {
	c, clock := controlTestController(t)
	var registry agent.NativeSessionEnrollmentRegistry = c
	require.False(t, registry.HasNativeSessionEnrollment(controlTestSession))
	status, _ := controlTestPost(t, c, "/register", controlTestFrame(), "", c.token)
	require.Equal(t, http.StatusOK, status)
	require.True(t, registry.HasNativeSessionEnrollment(controlTestSession), "native plugin registration precedes viewer inspection")
	require.False(t, registry.HasNativeSessionEnrollment("unrelated-generated-history"))

	state, err := c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	_, err = c.Send(t.Context(), controlTestInput(state, "registry-queued-input"))
	require.NoError(t, err)
	clock.Advance(time.Duration(contracts.NativeSessionControlLeaseSeconds+1) * time.Second).MustWait(testutil.DeadlineContext(t))
	require.True(t, registry.HasNativeSessionEnrollment(controlTestSession), "queued disconnected enrollment is not safe absence")
	state, err = c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	require.True(t, state.OwnerPresent)
	require.False(t, state.CanSend)
	require.NotEmpty(t, state.RecentInputs)
}
