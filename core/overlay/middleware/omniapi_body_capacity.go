package middleware

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"net/http"
	"sync/atomic"
)

type omniBodyCapacity struct {
	used  atomic.Int64
	limit int64
}

var omniLargeModelBodies = omniBodyCapacity{limit: 32 << 20}

const omniLargeBodyThreshold int64 = 1 << 20

// Run after authentication and before request archive/decoding. Large bodies
// retain their weighted slot until the request ends, including every retry.
// Small ordinary requests do not wait behind large-context generations.
func OmniModelBodyCapacity() gin.HandlerFunc {
	return omniModelBodyCapacityGuard(&omniLargeModelBodies, common.OmniModelRequestBodyBytes)
}

func omniModelBodyCapacityGuard(budget *omniBodyCapacity, maxBody int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Body == nil {
			c.Next()
			return
		}
		reject := func(status int, code, message string) {
			if c.Request.URL.Path == "/v1/messages" {
				c.AbortWithStatusJSON(status, gin.H{"type": "error", "error": gin.H{"type": "invalid_request_error", "code": code, "message": message}})
			} else {
				c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "code": code, "message": message}})
			}
		}
		size := c.Request.ContentLength
		if size > maxBody {
			reject(http.StatusRequestEntityTooLarge, "request_body_too_large", "请求体超过当前传输上限32MiB；模型上下文限制另行计算")
			return
		}
		if size <= 0 && c.Request.Body != http.NoBody {
			size = maxBody
		}
		weight := int64(0)
		if size >= omniLargeBodyThreshold {
			weight = ((size + omniLargeBodyThreshold - 1) / omniLargeBodyThreshold) * omniLargeBodyThreshold
		}
		if weight > 0 {
			for {
				used := budget.used.Load()
				if weight > budget.limit || used > budget.limit-weight {
					c.Header("Retry-After", "2")
					reject(http.StatusServiceUnavailable, "request_capacity_busy", "当前大请求处理容量已满，请稍后重试；本次尚未发送给模型")
					return
				}
				if budget.used.CompareAndSwap(used, used+weight) {
					break
				}
			}
			defer budget.used.Add(-weight)
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBody)
		c.Next()
	}
}
