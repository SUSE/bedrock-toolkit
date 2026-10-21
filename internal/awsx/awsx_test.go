package awsx

import (
	"errors"
	"fmt"
	"net"
	"testing"
)

func TestRetryerRetriesDNSNotFound(t *testing.T) {
	r := newRetryer()
	if r.MaxAttempts() != RetryMaxAttempts {
		t.Errorf("max attempts %d, want %d", r.MaxAttempts(), RetryMaxAttempts)
	}
	nx := fmt.Errorf("request send failed: %w", &net.OpError{Op: "dial", Net: "tcp",
		Err: &net.DNSError{Err: "no such host", Name: "b.s3.amazonaws.com", IsNotFound: true}})
	if !r.IsErrorRetryable(nx) {
		t.Error("a DNS no-such-host error must be retried")
	}
	if r.IsErrorRetryable(errors.New("access denied")) {
		t.Error("a plain error must not be retried")
	}
}
