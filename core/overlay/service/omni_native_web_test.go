package service

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"testing"
)

type webTestTransport func(*http.Request) (*http.Response, error)

func (f webTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeWebSessionBothParentsAndRefresh(t *testing.T) {
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.OmniNativeWebSession{}))
	parent, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test")
	require.NoError(t, err)
	web, err := CreateLoginSessionAtAuthVersion(user.Id, user.AuthVersion, "native:webview:bridge", "127.0.0.1", "test")
	require.NoError(t, err)
	identity, err := ParseAccessToken(web.AccessToken)
	require.NoError(t, err)
	require.Error(t, ValidateNativeWebSessionFromBundle(web)) // missing link fails closed
	require.NoError(t, model.DB.Create(&model.OmniNativeWebSession{SID: web.Session.SID, NativeSID: "native-parent", ParentSID: parent.Session.SID, UserID: user.Id, UserAuthVersion: user.AuthVersion, ParentVersion: 1}).Error)
	t.Setenv("OMNI_WEB_BRIDGE_SECRET", strings.Repeat("w", 32))
	old := nativeWebClient.Transport
	t.Cleanup(func() { nativeWebClient.Transport = old })
	live := true
	nativeWebClient.Transport = webTestTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "host.docker.internal:8791", r.URL.Host)
		require.Equal(t, strings.Repeat("w", 32), r.Header.Get("X-Omni-Web-Service"))
		require.Empty(t, r.Header.Get("Authorization"))
		raw := `{"success":false}`
		status := 401
		if live {
			raw = `{"success":true,"data":{"uid":1,"native_sid":"native-parent","parent_sid":"` + parent.Session.SID + `","uv":1,"sv":1}}`
			status = 200
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(raw)), Header: http.Header{}}, nil
	})
	_, _, err = ValidateLoginSession(identity)
	require.NoError(t, err)
	for i := 0; i < 60; i++ {
		renewed, err := ReissueNativeWebSession(&NativeWebIdentity{UID: user.Id, NativeSID: "native-parent", ParentSID: parent.Session.SID, UV: 1, SV: 1})
		require.NoError(t, err)
		require.Equal(t, web.Session.SID, renewed.Session.SID)
	}
	live = false
	_, _, err = ValidateLoginSession(identity)
	require.Error(t, err)
	_, _, err = RefreshLoginSession(web.RefreshToken, web.Session.SID, "127.0.0.1", "test")
	require.Error(t, err)
	live = true
	_, err = model.RevokeUserSession(user.Id, parent.Session.SID, "test")
	require.NoError(t, err)
	_, _, err = ValidateLoginSession(identity)
	require.Error(t, err)
}
