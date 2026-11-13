package service

import relaycommon "github.com/QuantumNous/new-api/relay/common"

// A token-priced search quote is enabled only after a supplier's actual search
// and billed token cost have been verified. The route requires a separate
// web_search_token_priced capability. Other hosted tools retain their charges.
func OmniTokenPricedWebSearch(info *relaycommon.RelayInfo,name string) bool {
 return info!=nil && (name=="web_search" || name=="web_search_preview") && omniQuoteRestriction(info,"hosted_web_search_token_priced")
}
