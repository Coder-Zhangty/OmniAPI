package model

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOmniSharedZeroUsageMembersAndPrivateProjection(t *testing.T) {
	_, id, _ := sharedFixture(t)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 91).Update("display_name", "研发池主").Error)
	require.NoError(t, DB.Create(&User{Id: 92, Username: "private@example.invalid", Email: "private@example.invalid", DisplayName: "private@example.invalid", AffCode: "usage92", Status: 1}).Error)
	require.NoError(t, DB.Create(&OmniSharedMember{PoolID: id, UserID: 92, State: "invited", BudgetMicros: -1}).Error)
	view, err := OmniSharedView(91, id, "")
	require.NoError(t, err)
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	var response map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &response))
	for _, key := range []string{"usage", "daily"} {
		require.JSONEq(t, "[]", string(response[key]))
	}
	require.Len(t, view["members"], 2)
	names := view["member_names"].(map[int]string)
	require.Equal(t, "研发池主", names[91])
	require.Equal(t, "用户 92", names[92])
	require.NotContains(t, string(raw), "private@example.invalid")
	require.NotContains(t, string(raw), "email")
	_, err = OmniSharedView(93, id, "")
	require.Error(t, err)
}
