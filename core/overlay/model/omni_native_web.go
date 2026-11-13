package model

// A WebView session must remain bound to both its browser approval and its
// native device session. No tokens or credentials are stored in this table.
type OmniNativeWebSession struct {
	SID             string `gorm:"column:sid;primaryKey;size:64"`
	NativeSID       string `gorm:"column:native_sid;size:64;index"`
	ParentSID       string `gorm:"column:parent_sid;size:64"`
	UserID          int
	UserAuthVersion int64
	ParentVersion   int64
}
