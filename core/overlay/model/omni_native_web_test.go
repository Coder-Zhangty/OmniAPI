package model

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"os"
	"testing"
)

func TestNativeWebMigrationUpgrade(t *testing.T) {
	var dialect gorm.Dialector = sqlite.Open(":memory:")
	switch os.Getenv("OMNI_WEB_TEST_DB") {
	case "mysql":
		dialect = mysql.Open(os.Getenv("OMNI_WEB_TEST_DSN"))
	case "postgres":
		dialect = postgres.Open(os.Getenv("OMNI_WEB_TEST_DSN"))
	}
	db, err := gorm.Open(dialect, &gorm.Config{})
	require.NoError(t, err)
	var version string
	query := "SELECT sqlite_version()"
	if db.Dialector.Name() == "postgres" {
		query = "SELECT version()"
	}
	if db.Dialector.Name() == "mysql" {
		query = "SELECT VERSION()"
	}
	require.NoError(t, db.Raw(query).Scan(&version).Error)
	t.Log("Database:", version)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Migrator().DropTable(&OmniNativeWebSession{}))
	// An existing account table and row represent data predating this additive
	// migration. The bridge may neither change nor lose that data.
	type existingAccount struct {
		ID   uint `gorm:"primaryKey"`
		Name string
	}
	require.NoError(t, db.AutoMigrate(&existingAccount{}))
	require.NoError(t, db.Where("id = ?", 1).Delete(&existingAccount{}).Error)
	require.NoError(t, db.Create(&existingAccount{ID: 1, Name: "existing-account"}).Error)
	for i := 0; i < 2; i++ {
		require.NoError(t, db.AutoMigrate(&OmniNativeWebSession{}))
	}
	row := OmniNativeWebSession{SID: "web-test", NativeSID: "native-test", ParentSID: "parent-test", UserID: 1, UserAuthVersion: 2, ParentVersion: 3}
	require.NoError(t, db.Create(&row).Error)
	require.NoError(t, db.AutoMigrate(&OmniNativeWebSession{}))
	var got OmniNativeWebSession
	require.NoError(t, db.Where("native_sid = ? AND parent_sid = ?", "native-test", "parent-test").First(&got).Error)
	require.Equal(t, row, got)
	var account existingAccount
	require.NoError(t, db.First(&account, 1).Error)
	require.Equal(t, "existing-account", account.Name)
	require.Error(t, db.Create(&row).Error) // primary-key uniqueness preserved
	require.True(t, db.Migrator().HasIndex(&OmniNativeWebSession{}, "NativeSID"))
}
