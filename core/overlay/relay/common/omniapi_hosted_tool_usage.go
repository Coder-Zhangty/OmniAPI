package common

import "sync"

// The shared pointer survives shallow per-channel RelayInfo clones. Old
// canceled attempts cannot overwrite the selected attempt's accounting facts.
type OmniHostedToolUsage struct {
 mu sync.Mutex
 attempt int
 search int
 fetch int
}
func (u *OmniHostedToolUsage) Begin() int {
 u.mu.Lock();defer u.mu.Unlock()
 u.attempt++;u.search=0;u.fetch=0;return u.attempt
}
func (u *OmniHostedToolUsage) Observe(attempt,search,fetch int) {
 u.mu.Lock();defer u.mu.Unlock()
 if attempt!=u.attempt{return}
 u.search=max(u.search,search);u.fetch=max(u.fetch,fetch)
}
func (u *OmniHostedToolUsage) Counts() (int,int) {
 if u==nil{return 0,0};u.mu.Lock();defer u.mu.Unlock();return u.search,u.fetch
}
