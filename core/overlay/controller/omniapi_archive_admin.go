package controller

import (
 "encoding/json"
 "io"
 "net/http"
 "os"
 "strings"
 "time"

 "github.com/gin-gonic/gin"
)

// Only non-sensitive collector health crosses into the gateway dashboard.
func OmniArchiveStatus(c *gin.Context) {
 origin := strings.TrimRight(os.Getenv("OMNI_ARCHIVE_COLLECTOR_URL"), "/")
 if origin == "" { c.JSON(503, gin.H{"success": false}); return }
 request, err := http.NewRequestWithContext(c.Request.Context(), "GET", origin+"/internal/metrics", nil)
 if err != nil { c.JSON(503, gin.H{"success": false}); return }
 request.Header.Set("Authorization", "Bearer "+os.Getenv("OMNI_ARCHIVE_COLLECTOR_TOKEN"))
 client := &http.Client{Timeout: 3*time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
 response, err := client.Do(request)
 if err != nil { c.JSON(503, gin.H{"success": false}); return }
 defer response.Body.Close()
 if response.StatusCode != 200 { c.JSON(503, gin.H{"success": false}); return }
 var metrics map[string]interface{}
 if json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&metrics) != nil { c.JSON(503, gin.H{"success": false}); return }
 data := gin.H{"capture_enabled": os.Getenv("OMNI_ARCHIVE_ENABLED") == "true"}
 for _, key := range []string{"queued_items", "queue_age_seconds", "free_bytes", "last_successful_sync", "replication_failures", "last_failure"} { data[key] = metrics[key] }
 c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}
