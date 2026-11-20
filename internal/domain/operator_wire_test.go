package domain

import "testing"

func TestOP17044Lease(t *testing.T) {
	s := Service{Cfg: DefaultConfig()}
	for _, tc := range []struct {
		seconds int
		valid   bool
	}{{1, false}, {14, false}, {15, true}, {300, true}, {301, false}} {
		got, err := s.leaseSeconds(&tc.seconds)
		if (err == nil) != tc.valid || (tc.valid && got != tc.seconds) {
			t.Fatalf("lease %d: got %d, err %v", tc.seconds, got, err)
		}
	}
	got, err := s.leaseSeconds(nil)
	if err != nil || got != int(s.Cfg.ClaimLeaseDefault.Seconds()) {
		t.Fatalf("default %d %v", got, err)
	}
}
