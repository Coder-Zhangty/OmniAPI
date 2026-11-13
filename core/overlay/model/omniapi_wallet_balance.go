package model

// One database snapshot keeps the available amount and pending reservations
// consistent. Holds change availability, while only settlement changes balance.
type OmniWalletBalance struct {
 AvailableQuota int64 `json:"available_quota"`
 ReservedQuota int64 `json:"reserved_quota"`
 BalanceQuota int64 `json:"balance_quota"`
}

func OmniWalletSnapshot(uid int) (OmniWalletBalance,error) {
 var snapshot OmniWalletBalance
 result:=DB.Raw("SELECT u.quota AS available_quota, COALESCE(SUM(r.reserved),0) AS reserved_quota, u.quota + COALESCE(SUM(r.reserved),0) AS balance_quota FROM users u LEFT JOIN omni_reservations r ON r.user_id=u.id AND r.source=? AND r.state=? WHERE u.id=? AND u.deleted_at IS NULL GROUP BY u.id,u.quota", "wallet","pending",uid).Scan(&snapshot)
 return snapshot,result.Error
}
