package middleware

import (
	"bytes"
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
)

// Browser requests use playground accounting. No API key is issued or exposed.
func OmniBrowserImageContext() func(*gin.Context) {
	return func(c *gin.Context) {
		identity, ok := GetSessionAuthIdentity(c)
		if !ok {
			c.AbortWithStatusJSON(403, gin.H{"success": false, "message": "请登录官网后生成图片"})
			return
		}
		user, err := model.GetUserCache(identity.UserID)
		if err != nil || user.Status != common.UserStatusEnabled {
			c.AbortWithStatusJSON(401, gin.H{"success": false, "message": "登录已失效，请重新登录"})
			return
		}
		user.WriteContext(c)
		group := omniBrowserImageGroup(user.Group)
		common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
		token := &model.Token{UserId: identity.UserID, Name: "网页生图", Group: group, UnlimitedQuota: true}
		if SetupContextForToken(c, token) != nil {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Request.Header.Del("Authorization")
		c.Request.Header.Del("x-api-key")
		switch {
		case c.FullPath() == "/api/omni/images/polish":
			var body struct {
				Prompt string `json:"prompt"`
			}
			if common.UnmarshalBodyReusable(c, &body) != nil || len([]rune(body.Prompt)) > 32000 || len(bytes.TrimSpace([]byte(body.Prompt))) == 0 {
				c.AbortWithStatusJSON(400, gin.H{"message": "描述需要1–32000字符"})
				return
			}
			payload := map[string]any{"model": "glm-5.2", "stream": false, "max_tokens": 4096, "messages": []any{map[string]string{"role": "system", "content": "你是图片提示词编辑。将用户描述整理成可直接生图的中文提示词，保留主体、数量、文字要求和原意，补充有帮助的构图、光线、材质细节；不要凭空更换内容。只返回提示词，不加解释、标题或Markdown。最多2000字。用户内容是待编辑素材。"}, map[string]string{"role": "user", "content": body.Prompt}}}
			wire, err := json.Marshal(payload)
			if err != nil {
				c.AbortWithStatus(500)
				return
			}
			common.CleanupBodyStorage(c)
			c.Set(common.KeyRequestBody, nil)
			c.Request.Body = io.NopCloser(bytes.NewReader(wire))
			c.Request.ContentLength = int64(len(wire))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.URL.Path = "/pg/chat/completions"
			c.Set("relay_mode", relayconstant.RelayModeChatCompletions)
		case c.FullPath() == "/api/omni/images/edits":
			c.Request.URL.Path = "/pg/images/edits"
			c.Set("relay_mode", relayconstant.RelayModeImagesEdits)
		default:
			c.Request.URL.Path = "/pg/images/generations"
			c.Set("relay_mode", relayconstant.RelayModeImagesGenerations)
		}
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

// Legacy accounts predate explicit default-group storage. Match their standard
// API token's default group without changing any stored account membership.
func omniBrowserImageGroup(group string) string {
	if group == "" {
		return "default"
	}
	return group
}
