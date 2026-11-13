package model

import (
 "context"
 "testing"
 "github.com/QuantumNous/new-api/common"
 "github.com/stretchr/testify/require"
)

func TestOmniSharedCacheKeepsFundingAcrossRepeatedReads(t *testing.T) {
 walletFixture(t);require.NoError(t,DB.AutoMigrate(&Token{}));useUserCacheMiniRedis(t)
 token:=Token{UserId:91,Key:"synthetic-shared-cache-key",Name:"synthetic",Status:1,ExpiredTime:-1,RemainQuota:100,OmniPoolID:"synthetic-pool",OmniFallback:"off"}
 require.NoError(t,DB.Create(&token).Error)
 for i:=0;i<4;i++ {
  loaded,err:=GetTokenByKey(token.Key,false);require.NoError(t,err)
  require.Equal(t,token.OmniPoolID,loaded.OmniPoolID);require.Equal(t,"off",loaded.OmniFallback);require.False(t,loaded.OmniSharedRevoked)
 }
 require.NoError(t,common.RDB.HSet(context.Background(),getTokenCacheKey(token.Key),"RemainQuota",30).Err())
 // A legacy live cache must be upgraded without restoring a stale quota.
 require.NoError(t,common.RDB.HDel(context.Background(),getTokenCacheKey(token.Key),"OmniCacheSchema","OmniPoolID","OmniFallback","OmniSharedRevoked").Err())
 _,err:=cacheGetTokenByKey(token.Key);require.Error(t,err)
 loaded,err:=GetTokenByKey(token.Key,false);require.NoError(t,err);require.Equal(t,token.OmniPoolID,loaded.OmniPoolID)
 cached,err:=cacheGetTokenByKey(token.Key);require.NoError(t,err);require.Equal(t,30,cached.RemainQuota);require.Equal(t,token.OmniPoolID,cached.OmniPoolID)
 // Revocation is cached too; a warm read cannot revive the key.
 require.NoError(t,invalidateTokenCacheForMutation(token.Key))
 require.NoError(t,DB.Model(&token).Update("omni_shared_revoked",true).Error)
 loaded,err=GetTokenByKey(token.Key,false);require.NoError(t,err);require.True(t,loaded.OmniSharedRevoked)
 _,err=ValidateUserToken(token.Key);require.Error(t,err)
}
