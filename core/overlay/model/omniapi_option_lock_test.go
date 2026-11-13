package model

import (
 "testing"
 "time"
 "github.com/QuantumNous/new-api/common"
)

// Pricing refresh reads Omni options while holding updatePricingLock. An option
// writer must release OptionMapRWMutex before it waits for that pricing lock.
func TestOmniBillingOptionDoesNotHoldOptionLockWhileInvalidatingPricing(t *testing.T) {
 common.OptionMapRWMutex.Lock()
 if common.OptionMap == nil { common.OptionMap = make(map[string]string) }
 previous := common.OptionMap["billing_setting.billing_mode"]
 common.OptionMapRWMutex.Unlock()
 updatePricingLock.Lock()
 done := make(chan error, 1)
 go func() { done <- updateOptionMap("billing_setting.billing_mode", "{}") }()
 // Give the writer a chance to reach cache invalidation, which cannot finish
 // until this test releases the pricing lock.
 time.Sleep(50 * time.Millisecond)
 read := make(chan struct{})
 go func() { GetOmniPolicy(); close(read) }()
 blocked := false
 select { case <-read: case <-time.After(300 * time.Millisecond): blocked = true }
 updatePricingLock.Unlock()
 select { case err := <-done: if err != nil { t.Fatal(err) }; case <-time.After(time.Second): t.Fatal("option writer did not finish") }
 <-read
 if err := updateOptionMap("billing_setting.billing_mode", previous); err != nil { t.Fatal(err) }
 if blocked { t.Fatal("option writer holds the global option lock while waiting for pricing refresh; lock order can deadlock") }
}
