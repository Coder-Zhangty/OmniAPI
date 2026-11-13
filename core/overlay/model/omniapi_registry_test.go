package model

import (
 "testing"
 "os"
 "github.com/glebarez/sqlite"
 "gorm.io/driver/mysql"
 "gorm.io/driver/postgres"
 "gorm.io/gorm"
 "github.com/QuantumNous/new-api/common"
 "github.com/stretchr/testify/require"
)
func TestOmniAliasPricing(t *testing.T) {
 common.OptionMapRWMutex.Lock()
 if common.OptionMap==nil{common.OptionMap=map[string]string{}}
 oldPrices,oldRegistry:=common.OptionMap["OmniAPIPricing"],common.OptionMap["OmniAPIModelRegistry"]
 common.OptionMap["OmniAPIPricing"]=`{"enabled":true,"version":"fixture","models":{"claude-opus-4-6":{}}}`
 common.OptionMap["OmniAPIModelRegistry"]=`{"enabled":true,"version":"registry-fixture","models":{"claude-opus-4-6":{"version":"4-6","capability":"text","aliases":["anthropic/claude-opus-4.6"],"bindings":[{"channel_id":7,"upstream_model":"anthropic/claude-opus-4.6","status":"verified","protocols":["openai"]}]}}}`
 common.OptionMapRWMutex.Unlock()
 t.Cleanup(func(){common.OptionMapRWMutex.Lock();common.OptionMap["OmniAPIPricing"]=oldPrices;common.OptionMap["OmniAPIModelRegistry"]=oldRegistry;common.OptionMapRWMutex.Unlock()})
 direct,_,_:=OmniPriceAvailability("claude-opus-4-6")
 alias,_,_:=OmniPriceAvailability("anthropic/claude-opus-4.6")
 require.Equal(t,"available",direct)
 require.Equal(t,direct,alias,"alias must use standard price")
limits:=OmniCanonicalLimit(map[string]bool{"anthropic/claude-opus-4.6":true,"claude-opus-4-7":false})
 require.Equal(t,map[string]bool{"claude-opus-4-6":true},limits,"normalization must retain the old permission scope")
 require.Equal(t,[]string{"claude-opus-4-6"},OmniCanonicalModels([]string{"anthropic/claude-opus-4.6","claude-opus-4-6"},true))
 previous:=GetOmniModelRegistry()
 common.OptionMapRWMutex.Lock();common.OptionMap["OmniAPIModelRegistry"]=`{"enabled":true,"version":"registry-new","models":{}}`;common.OptionMapRWMutex.Unlock()
 upstream,found:=OmniChannelUpstream(previous.Version,"claude-opus-4-6",7);require.True(t,found);require.Equal(t,"anthropic/claude-opus-4.6",upstream,"in-flight registry version is immutable")
 common.OptionMapRWMutex.Lock();common.OptionMap["OmniAPIModelRegistry"]=`{"enabled":true,"version":"registry-fixture","models":{"claude-opus-4-6":{"version":"4-6","capability":"text","aliases":["anthropic/claude-opus-4.6"],"bindings":[{"channel_id":7,"upstream_model":"anthropic/claude-opus-4.6","status":"verified","protocols":["openai"]}]}}}`;common.OptionMapRWMutex.Unlock()
 status,_,_:=OmniPriceAvailability("claude-opus-4-7");require.NotEqual(t,"available",status,"different version must stay distinct")
}

func TestOmniRegistryDiagnosticMigration(t *testing.T) {
 var dialect gorm.Dialector=sqlite.Open(":memory:")
 switch os.Getenv("OMNI_REGISTRY_TEST_DB") {case "mysql":dialect=mysql.Open(os.Getenv("OMNI_REGISTRY_TEST_DSN"));case "postgres":dialect=postgres.Open(os.Getenv("OMNI_REGISTRY_TEST_DSN"))}
 db,err:=gorm.Open(dialect,&gorm.Config{});require.NoError(t,err)
 sqlDB,err:=db.DB();require.NoError(t,err);t.Cleanup(func(){_ = sqlDB.Close()})
 require.NoError(t,db.Migrator().DropTable(&OmniDiagnosticAttempt{}))
 require.NoError(t,db.AutoMigrate(&OmniDiagnosticAttempt{}))
 record:=OmniDiagnosticAttempt{RequestID:"registry-fixed-channel",AdminID:1,ChannelID:7,Model:"test-model",State:"passed",InputTokens:1,OutputTokens:1,CostEstimated:true}
 require.NoError(t,db.Create(&record).Error)
 for range 2{require.NoError(t,db.AutoMigrate(&OmniDiagnosticAttempt{}))}
 var got OmniDiagnosticAttempt;require.NoError(t,db.Where("request_id = ?",record.RequestID).First(&got).Error);require.Equal(t,record,got)
 require.Nil(t,got.CostUSD,"unknown procurement must never become a zero price")
 duplicate:=record;duplicate.ID=0;require.Error(t,db.Create(&duplicate).Error)
 query:="SELECT sqlite_version()";if db.Dialector.Name()=="mysql"{query="SELECT VERSION()"};if db.Dialector.Name()=="postgres"{query="SELECT version()"};var version string;require.NoError(t,db.Raw(query).Scan(&version).Error);t.Log("Database:",version)
}
