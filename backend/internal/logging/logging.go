// Package logging adds one verbosity level to the standard logger.
//
// The frame is an appliance: when something is wrong the person debugging it is
// reading journalctl, not attaching a debugger. So the default level is
// deliberately talkative about anything that happens once per sync or less
// often. Debugf carries the rest — per-request and per-HTTP-call detail that
// would otherwise scroll the useful lines away.
package logging

import (
	"fmt"
	"log"
	"sync/atomic"
)

var verbose atomic.Bool

// SetVerbose turns Debugf on. Set once at startup from the config or
// FRAME_VERBOSE=1, so it can be flipped on a running frame without a rebuild.
func SetVerbose(on bool) { verbose.Store(on) }

func Enabled() bool { return verbose.Load() }

func Debugf(format string, args ...any) {
	if !verbose.Load() {
		return
	}
	log.Output(2, "debug: "+fmt.Sprintf(format, args...))
}
