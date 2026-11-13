package controller

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"net/http/httptest"
	"testing"
)

func TestOmniTopupOwnerAndAdminList(t *testing.T) {
	old := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	model.DB = db
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { model.DB = old; sql.Close() })
	if err = db.AutoMigrate(&model.OmniTopup{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&model.OmniTopup{ID: "xp_owner91", UserID: 91, RequestID: "r91", State: "pending"})
	db.Create(&model.OmniTopup{ID: "xp_owner92", UserID: 92, RequestID: "r92", State: "pending"})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("id", 91) })
	r.GET("/api/omni/topups/:id", OmniTopupGet)
	r.POST("/api/omni/topups/:id/check", OmniTopupCheck)
	r.GET("/api/omni/topups", OmniTopupList)
	r.GET("/api/omni/admin/topups", OmniTopupList)
	for _, method := range []string{"GET", "POST"} {
		path := "/api/omni/topups/xp_owner92"
		if method == "POST" {
			path += "/check"
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != 404 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for path, n := range map[string]int{"/api/omni/topups": 1, "/api/omni/admin/topups": 2} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var p struct {
			Data []model.OmniTopup `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &p) != nil || len(p.Data) != n {
			t.Fatal(w.Body.String())
		}
	}
}
