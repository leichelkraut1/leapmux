package agent

// NativeSessionEnrollmentRegistry reports sessions already enrolled through a
// provider's own local bridge, independently of browser inspection. Looking up
// a session must not create a bridge or enroll/resume a native owner. Stale or
// disconnected enrollments with pending input remain present until safely gone.
type NativeSessionEnrollmentRegistry interface {
	HasNativeSessionEnrollment(sessionID string) bool
}
