# gobreak
A useful circuit breaker library for HTTP requests to go through or not. Retry functionality included.
It has a lot of settings you can configure, but you can use it with defaults as well. 

First, import the library using the path ``github.com/KaiserWerk/gobreak``.

The main construct is the ``CircuitBreaker``. Use the function `New(s *Settings, rp *RetryPolicy) *CircuitBreaker` to create a new
instance. In order to use default values only, use nil for both parameters:

```golang
cb := gobreak.New(nil, nil)
```

You can use ``*Settings`` and a `*RetryPolicy` to configure your `CircuitBreaker` instance:

```golang
s := &gobreak.Settings{
    Transport: nil, // a custom transport for the default *http.Client
    MaxRequestsHalfOpen: 5,
    ResetIntervalClosed: 24 * time.Hour,
    WaitUntilHalfOpen:  5 * time.Minute,
    // ShouldTrip is called with a copy of Stats whenever a request fails while in closed state.
    // If ShouldTrip returns true, CircuitBreaker will be placed into the open state. If ShouldTrip is nil,
    // default ShouldTrip is used. Default ShouldTrip returns true when the number of consecutive
    // failures is 3 or more.
    ShouldTrip: nil, // func(stats Stats) bool
    // StateChanged is called in a goroutine whenever the State of CircuitBreaker changes.
    StateChanged: nil, // func(from, to State)
    // called with the error returned from a request. If DeemedSuccessful returns true, the error is counted as a
    // success. Otherwise, the error is counted as a failure. If DeemedSuccessful is nil, a default
    // DeemedSuccessful is used, which returns true for all nil errors, otherwise false.
    DeemedSuccessful: nil, // func(err error) bool
}
rp := &gobreak.RetryPolicy{
	MaxAttempts: 5,
	MinDelay: 2 * time.Second,
}
cb := gobreak.New(s, rp)
```

Once created, use it to execute requests:

```golang
// set up the CircuitBreaker
// ...

// create any request
req, _ := http.NewRequest(http.MethodGet, "https://some-url.com/path", nil)

// execute requests like your normally would with a http.Client
resp, err := cb.Do(req)
// do sth. with the result

```

