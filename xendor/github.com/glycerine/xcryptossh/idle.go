package ssh

import (
	"fmt"
	//"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

//func init() {
//  // see all goroutines on panic for proper debugging.
//	debug.SetTraceback("all")
//}

// idleTimer allows a client of the ssh
// library to notice if there has been a
// stall in i/o activity. This enables
// clients to impliment timeout logic
// that works and doesn't timeout under
// long-duration-but-still-successful
// reads/writes.
//
// It is probably simpler to use the
// SetIdleTimeout(dur time.Duration)
// method on the channel.
//
type idleTimer struct {
	mut             sync.Mutex
	idleDur         time.Duration
	last            uint64
	halt            *Halter
	timeoutCallback func()

	// GetIdleTimeoutCh returns the current idle timeout duration in use.
	// It will return 0 if timeouts are disabled.
	getIdleTimeoutCh chan time.Duration
	setIdleTimeoutCh chan time.Duration

	setCallback chan func()
}

// newIdleTimer creates a new idleTimer which will call
// the `callback` function provided after `dur` inactivity.
// If callback is nil, you must use setTimeoutCallback()
// to establish the callback before activating the timer
// with SetIdleTimeout. The `dur` can be 0 to begin with no
// timeout, in which case the timer will be inactive until
// SetIdleTimeout is called.
func newIdleTimer(callback func(), dur time.Duration) *idleTimer {
	t := &idleTimer{
		getIdleTimeoutCh: make(chan time.Duration),
		setIdleTimeoutCh: make(chan time.Duration),
		setCallback:      make(chan func()),
		halt:             NewHalter(),
		timeoutCallback:  callback,
	}
	go t.backgroundStart(dur)
	return t
}

func (t *idleTimer) setTimeoutCallback(f func()) {
	select {
	case t.setCallback <- f:
	case <-t.halt.ReqStop.Chan:
	}
}

// Reset stores the current monotonic timestamp
// internally, effectively reseting to zero the value
// returned from an immediate next call to NanosecSince().
//
func (t *idleTimer) Reset() {
	atomic.StoreUint64(&t.last, monoNow())
}

// NanosecSince returns how many nanoseconds it has
// been since the last call to Reset().
func (t *idleTimer) NanosecSince() uint64 {
	return monoNow() - atomic.LoadUint64(&t.last)
}

// SetIdleTimeout stores a new idle timeout duration. This
// activates the idleTimer if dur > 0. Set dur of 0
// to disable the idleTimer. A disabled idleTimer
// always returns false from TimedOut().
//
// This is the main API for idleTimer. Most users will
// only need to use this call.
//
func (t *idleTimer) SetIdleTimeout(dur time.Duration) {
	select {
	case t.setIdleTimeoutCh <- dur:
	case <-t.halt.ReqStop.Chan:
	}
}

// GetIdleTimeout returns the current idle timeout duration in use.
// It will return 0 if timeouts are disabled.
func (t *idleTimer) GetIdleTimeout() (dur time.Duration) {
	select {
	case dur = <-t.getIdleTimeoutCh:
	case <-t.halt.ReqStop.Chan:
	}
	return
}

// TimedOut returns true if it has been longer
// than t.GetIdleDur() since the last call to t.Reset().
func (t *idleTimer) TimedOut() bool {

	var dur time.Duration
	select { // hung here, so not unlocking... is our goro not live???, nope our goro died.
	case dur = <-t.getIdleTimeoutCh:
	case <-t.halt.ReqStop.Chan:
		return false
		// I think at the end of long test, this was
		// timeout out, causing us to produce the wrong result.
		//	case <-time.After(10 * time.Second):
		//		// assume its not active???
		//		return false
	}
	if dur == 0 {
		return false
	}
	return t.NanosecSince() > uint64(dur)
}

func (t *idleTimer) Stop() {
	t.halt.ReqStop.Close()
	select {
	case <-t.halt.Done.Chan:
	case <-time.After(10 * time.Second):
		panic("idleTimer.Stop() problem! t.halt.Done.Chan not received  after 10sec! serious problem")
	}
}

func (t *idleTimer) backgroundStart(dur time.Duration) {
	go func() {
		var heartbeat *time.Ticker
		var heartch <-chan time.Time
		if dur > 0 {
			heartbeat = time.NewTicker(dur)
			heartch = heartbeat.C
		}
		defer func() {
			fmt.Printf("\n\n backgroundStart goro is exiting!!! \n\n")
			if heartbeat != nil {
				heartbeat.Stop() // allow GC
			}
			t.halt.Done.Close()
		}()
		for {
			select {
			case <-t.halt.ReqStop.Chan:
				return

			case f := <-t.setCallback:
				t.timeoutCallback = f

			case t.getIdleTimeoutCh <- dur:
				fmt.Printf("\n\n backgroundStart goro sent dur %v on getIdleTimeoutCh\n\n", dur)
				// nothing more
			case newdur := <-t.setIdleTimeoutCh:
				if dur > 0 {
					// timeouts active currently
					if newdur == dur {
						continue
					}
					if newdur <= 0 {
						// stopping timeouts
						if heartbeat != nil {
							heartbeat.Stop() // allow GC
						}
						dur = newdur
						heartbeat = nil
						heartch = nil

						// since we were just using timeouts, the machinery
						// may still be stuck waiting for one. nudge it now

						//					fmt.Printf("\n\n idleTimer: go t.timeoutCallback() being " +
						//						"called now: timer going from active to inactive!\n\n")
						//					go t.timeoutCallback()

						continue
					}
					// changing an active timeout dur
					if heartbeat != nil {
						heartbeat.Stop() // allow GC
					}
					dur = newdur
					heartbeat = time.NewTicker(dur)
					heartch = heartbeat.C
					t.Reset()
					continue
				} else {
					// heartbeats not currently active
					if newdur <= 0 {
						dur = 0
						// staying inactive
						continue
					}
					// heartbeats activating
					dur = newdur
					heartbeat = time.NewTicker(dur)
					heartch = heartbeat.C
					t.Reset()

					//fmt.Printf("\n\n idleTimer: go t.timeoutCallback() begin called now: timer going from inactive to active!\n\n")
					//go t.timeoutCallback()

					continue
				}

			case <-heartch:
				if dur == 0 {
					panic("should be impossible to get heartbeat.C on dur == 0")
				}
				if t.NanosecSince() > uint64(dur) {
					// After firing, disable until reactivated.
					// Still must be a ticker and not a one-shot because it may take
					// many, many heartbeats before a timeout, if one happens
					// at all.
					if heartbeat != nil {
						heartbeat.Stop() // allow GC
					}
					heartbeat = nil
					heartch = nil
					if t.timeoutCallback == nil {
						panic("idleTimer.timeoutCallback was never set! call t.setTimeoutCallback()!!!")
					}
					// our caller may be holding locks...
					// and timeoutCallback will want locks...
					// so unless we start timeoutCallback() on its
					// own goroutine, we are likely to deadlock.
					fmt.Printf("\n\n idleTimer: go t.timeoutCallback() begin called now! heartbeat happened after timeout.\n\n")
					go t.timeoutCallback()
				}
			}
		}
	}()
}