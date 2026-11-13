package middleware

import (
 "net/http/httptest"
 "testing"
 "github.com/gin-gonic/gin"
)

func TestOmniBrowserImagesRequireSessionBeforeRelay(t *testing.T) {
 gin.SetMode(gin.TestMode)
 r:=gin.New();called:=false
 r.POST("/images",OmniBrowserImageContext(),func(c *gin.Context){called=true})
 w:=httptest.NewRecorder();req:=httptest.NewRequest("POST","/images",nil)
 req.Header.Set("Authorization","Bearer unrelated-token")
 r.ServeHTTP(w,req)
 if w.Code!=403||called {t.Fatal("non-session request reached image relay",w.Code)}
}

func TestOmniBrowserImagesLegacyDefaultGroup(t *testing.T) {
 if omniBrowserImageGroup("")!="default" {t.Fatal("legacy account cannot use standard default routes")}
 if omniBrowserImageGroup("custom")!="custom" {t.Fatal("custom membership changed")}
}
