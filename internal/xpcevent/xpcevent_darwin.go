//go:build darwin && cgo

package xpcevent

/*
#include <stdlib.h>
#include <dispatch/dispatch.h>
#include <xpc/xpc.h>

static dispatch_semaphore_t summond_event_sem;

static void summond_set_stream_handler(const char *stream) {
	summond_event_sem = dispatch_semaphore_create(0);
	xpc_set_event_stream_handler(stream, dispatch_get_global_queue(QOS_CLASS_DEFAULT, 0), ^(xpc_object_t event) {
		dispatch_semaphore_signal(summond_event_sem);
	});
}

static int summond_wait_for_event(int64_t timeout_ms) {
	dispatch_time_t deadline = dispatch_time(DISPATCH_TIME_NOW, timeout_ms * NSEC_PER_MSEC);
	return dispatch_semaphore_wait(summond_event_sem, deadline) == 0 ? 1 : 0;
}
*/
import "C"

import (
	"time"
	"unsafe"
)

// ConsumeStream registers a handler for the named launchd XPC event stream
// and waits up to timeout for the pending event to arrive. A job launched
// for a LaunchEvents event must consume that event this way; otherwise
// launchd treats it as undelivered and relaunches the job indefinitely.
// Returns true if an event was delivered within the timeout.
func ConsumeStream(stream string, timeout time.Duration) bool {
	cs := C.CString(stream)
	defer C.free(unsafe.Pointer(cs))
	C.summond_set_stream_handler(cs)
	return C.summond_wait_for_event(C.int64_t(timeout/time.Millisecond)) == 1
}
