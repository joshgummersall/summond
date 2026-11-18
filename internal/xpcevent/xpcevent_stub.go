//go:build !darwin || !cgo

package xpcevent

import "time"

// ConsumeIOKitMatching is a no-op without cgo on darwin; launchd event consumption
// is unavailable, so event-triggered jobs may be relaunched by launchd.
func ConsumeIOKitMatching(timeout time.Duration) bool {
	return false
}
