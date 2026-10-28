package codexobserve

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifyPeer(t *testing.T) {
	left, right := net.Pipe()
	t.Cleanup(func() { _ = right.Close() })
	expected := peerIdentity(12, 1234, "ChatGPT.exe")
	valid := true
	released := 0
	conn := &verifiedConn{Conn: left, peer: expected, verify: func(context.Context) error {
		if !valid {
			return errors.New("identity changed")
		}
		return nil
	}, release: func() { released++ }}
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, VerifyPeer(context.Background(), conn, expected))
	changed := expected
	changed.ProcessStartTime++
	require.Error(t, VerifyPeer(context.Background(), conn, changed))
	require.Error(t, VerifyPeer(context.Background(), right, expected))
	valid = false
	require.Error(t, VerifyPeer(context.Background(), conn, expected))
	valid = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, VerifyPeer(ctx, conn, expected), context.Canceled)
	require.NoError(t, conn.Close())
	require.NoError(t, conn.Close())
	require.Equal(t, 1, released)
	require.Error(t, VerifyPeer(context.Background(), conn, expected))
}

func TestPeerIdentityDoesNotExposePaths(t *testing.T) {
	a := peerIdentity(10, 20, "ChatGPT")
	b := peerIdentity(10, 21, "ChatGPT")
	require.NotEqual(t, a.SourceID, b.SourceID)
	require.Regexp(t, "^codex-ipc-[0-9a-f]{32}$", a.SourceID)
}

func TestWindowsImageName(t *testing.T) {
	const program = "C:\\Program Files"
	const local = "C:\\Users\\example\\AppData\\Local"
	tests := []struct {
		path    string
		allowed bool
	}{
		{program + "\\WindowsApps\\OpenAI.Codex_26.1021.3705.0_x64__2p2nqsd0c76g0\\app\\ChatGPT.exe", true},
		{program + "\\WindowsApps\\OpenAI.Codex_26.1021.3705.0_arm64__2p2nqsd0c76g0\\app\\ChatGPT.exe", true},
		{local + "\\Programs\\Microsoft VS Code\\Code.exe", true},
		{program + "\\Microsoft VS Code\\Code.exe", true},
		{program + "\\WindowsApps\\OpenAI.Codex_26.1021.3705.0_x64__wrong\\app\\ChatGPT.exe", false},
		{program + "\\WindowsApps\\Other_1.0.0.0_x64__2p2nqsd0c76g0\\app\\ChatGPT.exe", false},
		{local + "\\Temp\\ChatGPT.exe", false},
		{local + "\\Programs\\Microsoft VS Code Evil\\Code.exe", false},
		{program + "\\Microsoft VS Code\\..\\Other\\Code.exe", false},
		{"\\\\server\\share\\Microsoft VS Code\\Code.exe", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			_, ok := windowsImageName(tt.path, program, "C:\\Program Files (x86)", local)
			require.Equal(t, tt.allowed, ok)
		})
	}
}

func TestDarwinImageName(t *testing.T) {
	for _, value := range []string{
		"/Applications/ChatGPT.app/Contents/MacOS/ChatGPT",
		"/Applications/Visual Studio Code.app/Contents/MacOS/Code",
		"/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper (Plugin).app/Contents/MacOS/Code Helper (Plugin)",
	} {
		_, ok := darwinImageName(value)
		require.True(t, ok, value)
	}
	for _, value := range []string{
		"/tmp/ChatGPT", "/Applications/Other.app/Contents/MacOS/ChatGPT",
		"/Applications/ChatGPT.app/../Other.app/Contents/MacOS/ChatGPT",
		"/Users/example/Downloads/Visual Studio Code.app/Contents/MacOS/Code",
	} {
		_, ok := darwinImageName(value)
		require.False(t, ok, value)
	}
}
