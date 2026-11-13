package service

import (
	"testing"
	"time"
)

func TestOmniFallbackSupplierRequiresExplicitSoleRouteException(t *testing.T) {
	p := OmniRoutePolicy{FallbackOnlyChannels: []int{7}, PrimaryExceptions: []string{"sole-model"}, Models: map[string][]int{"claude": {6, 1, 7}, "sole-model": {7}}}
	if err := ValidateOmniSupplierOrder(p); err != nil { t.Fatal(err) }
	p.Models["claude"] = []int{7, 1, 6}
	if ValidateOmniSupplierOrder(p) == nil { t.Fatal("expensive supplier became primary") }
	p.Models["claude"] = []int{6, 7, 1}
	if ValidateOmniSupplierOrder(p) == nil { t.Fatal("expensive supplier bypassed cheaper fallback") }
	p.Models["claude"] = []int{6, 1, 7}
	p.Models["new-model"] = []int{7}
	if ValidateOmniSupplierOrder(p) == nil { t.Fatal("sole route silently published without exception") }
}

func TestOmniCircuit(t *testing.T) {
	p := OmniRoutePolicy{FailureThreshold: 3, CooldownSeconds: 60}
	now := time.Now()
	key := "test-circuit"
	for i := 0; i < 3; i++ {
		OmniCircuitResult(key, false, now, p)
	}
	if OmniCircuitAllow(key, now) {
		t.Fatal("not open")
	}
	if !OmniCircuitAllow(key, now.Add(61*time.Second)) {
		t.Fatal("no half-open probe")
	}
	if OmniCircuitAllow(key, now.Add(61*time.Second)) {
		t.Fatal("parallel probe")
	}
	OmniCircuitResult(key, true, now, p)
	if !OmniCircuitAllow(key, now) {
		t.Fatal("did not recover")
	}
}

func TestOmniCancelledHalfOpenProbeCanRetry(t *testing.T) {
	p := OmniRoutePolicy{FailureThreshold: 3, CooldownSeconds: 60}
	now := time.Now()
	key := "cancel-probe"
	for i := 0; i < 3; i++ {
		OmniCircuitResult(key, false, now, p)
	}
	later := now.Add(61 * time.Second)
	if !OmniCircuitAllow(key, later) {
		t.Fatal("probe denied")
	}
	OmniCircuitNeutral(key)
	if !OmniCircuitAllow(key, later) {
		t.Fatal("cancelled probe locked circuit")
	}
}
