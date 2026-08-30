//go:build !darwin || !cgo

package xpcevent

import "time"

// ConsumeStream is a no-op without cgo on darwin; launchd event consumption
// is unavailable, so event-triggered jobs may be relaunched by launchd.
func ConsumeStream(stream string, timeout time.Duration) bool {
	return false
}
