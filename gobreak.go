package gobreak

import (
	"context"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

type Disallowed struct{}

func (d Disallowed) Error() string {
	return "request was disallowed to be executed due to policy"
}

type (
	// State describes the internal state of the CircuitBreaker, meaning open, half-open or closed.
	State uint8
	// CircuitBreaker - the central topic
	CircuitBreaker struct {
		httpClient *http.Client

		state      State
		stateMutex sync.RWMutex

		stats *statsInternal

		cleanupTicker *time.Ticker
		cleanupMutex  sync.RWMutex
		cleanupFunc   func()

		maxRequestsHalfOpen uint64
		resetIntervalClosed time.Duration
		waitUntilHalfOpen   time.Duration

		shouldTrip       func(stats Stats) bool
		stateChanged     func(from State, to State)
		deemedSuccessful func(err error) bool

		retryAttempts uint64
		retryMinDelay time.Duration
	}
	// Settings supplies means to define the behaviour of the CircuitBreaker. All values are optional and if missing,
	// are set to sensible defaults.
	Settings struct {
		// Transport is a custom transport which will be used for requests. You can still supply another custom
		// *http.Client to any specific request, though.
		// If not nil, a new *http.Client using this Transport with a Timeout of 10 seconds is created and
		// used subsequently.
		Transport *http.Transport
		// MaxRequestsHalfOpen is the maximum number of requests allowed to pass through when the CircuitBreaker is half-open.
		// If MaxRequestsHalfOpen is 0, CircuitBreaker allows only 1 request.
		MaxRequestsHalfOpen uint64
		// ResetInterval describes the interval of the closed state for CircuitBreaker to clear the internal statsInternal.
		// If Interval is 0, CircuitBreaker doesn't clear the internal statsInternal during the closed state at all.
		ResetIntervalClosed time.Duration
		// period of the open state, after which the state of CircuitBreaker becomes half-open. If WaitUntilHalfOpen is 0,
		// the default value of 1 minute is used.
		WaitUntilHalfOpen time.Duration
		// ShouldTrip is called with a copy of statsInternal whenever a request fails while in closed state.
		// If ShouldTrip returns true, CircuitBreaker will be placed into the open state. If ShouldTrip is nil,
		// default ShouldTrip is used. Default ShouldTrip returns true when the number of consecutive
		// failures is 3 or more.
		ShouldTrip func(stats Stats) bool
		// StateChanged is called in a goroutine whenever the State of CircuitBreaker changes.
		StateChanged func(from, to State)
		// called with the error returned from a request. If DeemedSuccessful returns true, the error is counted as a
		// success. Otherwise, the error is counted as a failure. If DeemedSuccessful is nil, a default
		// DeemedSuccessful is used, which returns true for all nil errors, otherwise false.
		DeemedSuccessful func(err error) bool
	}
	// The RetryPolicy defines the behaviour of the internal retry logic. If omitted, useful default values
	// are used.
	RetryPolicy struct {
		// The maximum attempts to execute an HTTP request until it is deemed a failure. Default value is 5.
		MaxAttempts uint64
		// The minimum delay to wait unti an attempts is retried. Subsequent attempts will wait slightly
		// longer by some randomly-decided amount of time (jitter).
		// If not set, a default base delay is used (2 seconds).
		MinDelay time.Duration
	}
	// statsInternal contains the relevant counted values of (un)successful (consecutive) and total requests.
	statsInternal struct {
		requests                uint64
		totalSuccesses          uint64
		totalFailures           uint64
		consecutiveSuccesses    uint64
		consecutiveFailures     uint64
		currentRequestsHalfOpen uint64
		lastRequestSuccessful   bool
		mut                     sync.RWMutex
	}

	Stats struct {
		Requests                uint64
		TotalSuccesses          uint64
		TotalFailures           uint64
		ConsecutiveSuccesses    uint64
		ConsecutiveFailures     uint64
		CurrentRequestsHalfOpen uint64
		LastRequestSuccessful   bool
	}
)

const (
	StateClosed State = iota
	StateHalfOpen
	StateOpen
)

// New returns a new *CircuitBreaker. Easy.
func New(s *Settings, rp *RetryPolicy) *CircuitBreaker {
	if s == nil {
		s = &Settings{}
	}
	cb := CircuitBreaker{
		maxRequestsHalfOpen: s.MaxRequestsHalfOpen,
		resetIntervalClosed: s.ResetIntervalClosed,
		waitUntilHalfOpen:   s.WaitUntilHalfOpen,
		shouldTrip:          s.ShouldTrip,
		stateChanged:        s.StateChanged,
		deemedSuccessful:    s.DeemedSuccessful,
	}

	if s.MaxRequestsHalfOpen == 0 {
		cb.maxRequestsHalfOpen = 1
	}
	if s.ResetIntervalClosed > 0 {
		cb.cleanupTicker = time.NewTicker(s.ResetIntervalClosed)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-cb.cleanupTicker.C:
					cb.cleanupMutex.Lock()
					cb.stats = &statsInternal{}
					cb.cleanupMutex.Unlock()
				}
			}
		}()
		cb.cleanupFunc = cancel
	}
	if s.WaitUntilHalfOpen == 0 {
		cb.waitUntilHalfOpen = 1 * time.Minute
	}

	if cb.shouldTrip == nil {
		cb.shouldTrip = defaultShouldTrip
	}
	if cb.stateChanged == nil {
		cb.stateChanged = defaultStateChanged
	}
	if cb.deemedSuccessful == nil {
		cb.deemedSuccessful = defaultDeemedSuccessful
	}

	cb.httpClient = &http.Client{
		Timeout: 10 * time.Second,
	}
	if s.Transport != nil {
		cb.httpClient.Transport = s.Transport
	}

	if rp != nil {
		cb.retryAttempts = rp.MaxAttempts
		cb.retryMinDelay = rp.MinDelay
	} else {
		cb.retryAttempts = 5
		cb.retryMinDelay = 2 * time.Second
	}

	return &cb
}

func (st *statsInternal) addRequest() {
	st.mut.Lock()
	st.requests++
	st.mut.Unlock()
}

func (st *statsInternal) addSuccess() {
	st.mut.Lock()
	st.totalSuccesses++
	st.consecutiveSuccesses++
	st.consecutiveFailures = 0
	st.mut.Unlock()
}

func (st *statsInternal) addFailure() {
	st.mut.Lock()
	st.totalFailures++
	st.consecutiveFailures++
	st.consecutiveSuccesses = 0
	st.mut.Unlock()
}

func (st *statsInternal) resetHalfOpenRequests() {
	st.mut.Lock()
	st.currentRequestsHalfOpen = 0
	st.mut.Unlock()
}

func (st *statsInternal) GetTotalRequests() uint64 {
	st.mut.RLock()
	defer st.mut.RUnlock()
	return st.requests
}

func (st *statsInternal) GetTotalSuccesses() uint64 {
	st.mut.RLock()
	defer st.mut.RUnlock()
	return st.totalSuccesses
}

func (st *statsInternal) GetTotalFailures() uint64 {
	st.mut.RLock()
	defer st.mut.RUnlock()
	return st.totalFailures
}

func (st *statsInternal) GetConsecutiveSuccesses() uint64 {
	st.mut.RLock()
	defer st.mut.RUnlock()
	return st.consecutiveSuccesses
}

func (st *statsInternal) GetConsecutiveFailures() uint64 {
	st.mut.RLock()
	defer st.mut.RUnlock()
	return st.consecutiveFailures
}

func (st *statsInternal) GetCurrentRequestsHalfOpen() uint64 {
	st.mut.RLock()
	defer st.mut.RUnlock()
	return st.currentRequestsHalfOpen
}

func (st *statsInternal) WasLastRequestSuccessful() bool {
	st.mut.RLock()
	defer st.mut.RUnlock()
	return st.lastRequestSuccessful
}

func (st *statsInternal) toPublicStats() Stats {
	st.mut.RLock()
	defer st.mut.RUnlock()
	return Stats{
		Requests:                st.requests,
		TotalSuccesses:          st.totalSuccesses,
		TotalFailures:           st.totalFailures,
		ConsecutiveSuccesses:    st.consecutiveSuccesses,
		ConsecutiveFailures:     st.consecutiveFailures,
		CurrentRequestsHalfOpen: st.currentRequestsHalfOpen,
		LastRequestSuccessful:   st.lastRequestSuccessful,
	}
}

// Close cleans up there is to end this properly.
func (cb *CircuitBreaker) Close() error {
	cb.cleanupFunc()
	return nil
}

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateHalfOpen:
		return "half-open"
	case StateOpen:
		return "open"
	}

	return "invalid state!"
}

// State returns the current State of the circuit breaker.
func (cb *CircuitBreaker) State() State {
	return cb.state
}

// Stats returns the CircuitBreaker's statsInternal as a copy.
func (cb *CircuitBreaker) Stats() Stats {
	return cb.stats.toPublicStats()
}

func (cb *CircuitBreaker) Do(r *http.Request, cl *http.Client) (*http.Response, error) {
	var client *http.Client
	if cl != nil {
		client = cl
	} else {
		client = cb.httpClient
	}

	// does the current state allow the request to be executed?
	if !cb.AllowRequest() {
		return nil, Disallowed{}
	}

	cb.stats.addRequest()

	// retry policy
	resp, err := retry(cb.retryAttempts, cb.retryMinDelay, r, client)
	// deemed successful policy
	if !cb.deemedSuccessful(err) {
		cb.stats.addFailure()

		if cb.State() == StateClosed && cb.shouldTrip(cb.stats.toPublicStats()) {
			cb.setState(StateOpen)
		}
		return nil, err
	}

	// reset the request count for half-open state and the state to closed if the number of successful requests
	// is greater than or equal to CircuitBreaker.maxRequestsHalfOpen
	if cb.state == StateHalfOpen && cb.stats.currentRequestsHalfOpen >= cb.maxRequestsHalfOpen {
		cb.setState(StateClosed)
		cb.stats.resetHalfOpenRequests()
	}
	
	cb.stats.addSuccess()

	return resp, nil
}

func (cb *CircuitBreaker) setState(s State) {
	cb.stateMutex.Lock()
	cb.state = s
	cb.stateMutex.Unlock()
}

func (cb *CircuitBreaker) AllowRequest() bool {
	cb.stateMutex.RLock()
	defer cb.stateMutex.RUnlock()
	if cb.state == StateClosed {
		return true
	}
	if cb.state == StateOpen {
		return false
	}
	if cb.state == StateHalfOpen && cb.maxRequestsHalfOpen > cb.stats.currentRequestsHalfOpen { // TODO: rework
		cb.stats.currentRequestsHalfOpen++
		return true
	}
	return false
}

func retry(attempts uint64, wait time.Duration, r *http.Request, client *http.Client) (*http.Response, error) {
	resp, err := client.Do(r)
	if err != nil {
		if attempts--; attempts > 0 {
			// prevent a thundering Herd
			jitter := time.Duration(rand.Int63n(int64(wait)))
			wait += jitter / 2

			time.Sleep(wait)
			return retry(attempts, 2*wait, r, client)
		}
		return nil, err
	}

	return resp, nil
}

func defaultShouldTrip(stats Stats) bool {
	return stats.ConsecutiveFailures >= 3
}

func defaultStateChanged(_, _ State) { /* does nothing */ }

func defaultDeemedSuccessful(err error) bool {
	return err == nil
}
