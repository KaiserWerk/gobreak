package gobreak

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func getListenerAndPortForTest() (net.Listener, int, error) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, 0, err
	}

	return listener, listener.Addr().(*net.TCPAddr).Port, nil
}

func Test_CircuitBreaker_Do(t *testing.T) {
	router := http.NewServeMux()
	router.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {})
	srv := httptest.NewServer(router)
	defer srv.Close()

	type args struct {
		cb      *CircuitBreaker
		r       *http.Request
		cl      *http.Client
		repeats uint
	}
	tests := []struct {
		name      string
		args      args
		wantState State
		wantErr   bool
	}{
		{
			"success",
			args{
				cb:      New(nil, nil),
				r:       httptest.NewRequest(http.MethodGet, fmt.Sprintf("%s/ok", srv.URL), nil),
				cl:      &http.Client{Timeout: 2 * time.Second},
				repeats: 20,
			},
			StateOpen,
			false,
		},
		{
			"trip after 3 failures",
			args{
				cb:      New(nil, nil),
				r:       httptest.NewRequest(http.MethodGet, fmt.Sprintf("%s/somepath", srv.URL), nil),
				cl:      &http.Client{Timeout: 2 * time.Second},
				repeats: 10,
			},
			StateClosed,
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.args.cb.Do(tc.args.r, tc.args.cl)
			if (err != nil) != tc.wantErr {
				t.Errorf("Do() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			got.Body.Close()
			if tc.wantState != tc.args.cb.State() {
				t.Errorf("expected state %v, got %v", tc.wantState, tc.args.cb.State())
			}
		})
	}
}
