package claude

// Plugin registration does not pass through Manager's viewer-inspection path.
// Include disconnected peers and pending inputs: a lost connection cannot prove
// that the original owner has exited or that its input is safe to resume.
func (c *nativeControlController) HasNativeSessionEnrollment(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peers[sessionID] != nil
}
