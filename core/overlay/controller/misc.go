package controller

import (
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/console_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

func TestStatus(c *gin.Context) {
	err := model.PingDB()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "数据库连接失败",
		})
		return
	}
	// 获取HTTP统计信息
	httpStats := middleware.GetStats()
	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"message":    "Server is running",
		"http_stats": httpStats,
	})
	return
}

func GetStatus(c *gin.Context) {

	cs := console_setting.GetConsoleSetting()
	passkeySetting := system_setting.PasskeySettingsSnapshot()
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()

	legalSetting := system_setting.GetLegalSettings()

	data := gin.H{
		"version":                     common.Version,
		"start_time":                  common.StartTime,
		"email_verification":          common.EmailVerificationEnabled,
		"github_oauth":                common.GitHubOAuthEnabled,
		"github_client_id":            common.GitHubClientId,
		"discord_oauth":               system_setting.GetDiscordSettings().Enabled,
		"discord_client_id":           system_setting.GetDiscordSettings().ClientId,
		"linuxdo_oauth":               common.LinuxDOOAuthEnabled,
		"linuxdo_client_id":           common.LinuxDOClientId,
		"linuxdo_minimum_trust_level": common.LinuxDOMinimumTrustLevel,
		"telegram_oauth":              common.TelegramOAuthEnabled,
		"telegram_oauth_configured":   oauth.TelegramConfigurationError() == nil,
		"telegram_bot_name":           common.TelegramBotName,
		"theme":                       "default",
		"system_name":                 common.SystemName,
		"logo":                        common.Logo,
		"footer_html":                 common.Footer,
		"wechat_qrcode":               common.WeChatAccountQRCodeImageURL,
		"wechat_login":                common.WeChatAuthEnabled,
		"server_address":              system_setting.ServerAddress,
		"turnstile_check":             common.TurnstileCheckEnabled,
		"turnstile_site_key":          common.TurnstileSiteKey,
		"docs_link":                   operation_setting.GetGeneralSetting().DocsLink,
		"quota_per_unit":              common.QuotaPerUnit,
		// 兼容旧前端：保留 display_in_currency，同时提供新的 quota_display_type
		"display_in_currency":           operation_setting.IsCurrencyDisplay(),
		"quota_display_type":            operation_setting.GetQuotaDisplayType(),
		"custom_currency_symbol":        operation_setting.GetGeneralSetting().CustomCurrencySymbol,
		"custom_currency_exchange_rate": operation_setting.GetGeneralSetting().CustomCurrencyExchangeRate,
		"enable_batch_update":           common.BatchUpdateEnabled,
		"enable_drawing":                common.DrawingEnabled,
		"enable_task":                   common.TaskEnabled,
		"enable_data_export":            common.DataExportEnabled,
		"data_export_default_time":      common.DataExportDefaultTime,
		"default_collapse_sidebar":      common.DefaultCollapseSidebar,
		"mj_notify_enabled":             setting.MjNotifyEnabled,
		"chats":                         setting.Chats,
		"demo_site_enabled":             operation_setting.DemoSiteEnabled,
		"self_use_mode_enabled":         operation_setting.SelfUseModeEnabled,
		"register_enabled":              common.RegisterEnabled,
		"password_login_enabled":        common.PasswordLoginEnabled,
		"password_register_enabled":     common.PasswordRegisterEnabled,
		"default_use_auto_group":        setting.DefaultUseAutoGroup,

		"password_login_encryption_enabled": common.PasswordLoginEncryptionEnabled,

		"usd_exchange_rate": operation_setting.USDExchangeRate,
		"price":             operation_setting.Price,
		"stripe_unit_price": setting.StripeUnitPrice,

		// 面板启用开关
		"api_info_enabled":      cs.ApiInfoEnabled,
		"uptime_kuma_enabled":   cs.UptimeKumaEnabled,
		"announcements_enabled": cs.AnnouncementsEnabled,
		"faq_enabled":           cs.FAQEnabled,

		// 模块管理配置
		"HeaderNavModules":    common.OptionMap["HeaderNavModules"],
		"SidebarModulesAdmin": common.OptionMap["SidebarModulesAdmin"],

		"oidc_enabled":                system_setting.GetOIDCSettings().Enabled,
		"oidc_client_id":              system_setting.GetOIDCSettings().ClientId,
		"oidc_authorization_endpoint": system_setting.GetOIDCSettings().AuthorizationEndpoint,
		"oidc_display_name":           system_setting.GetOIDCSettings().GetEffectiveDisplayName(),
		"passkey_login":               passkeySetting.Enabled,
		"passkey_display_name":        passkeySetting.RPDisplayName,
		"passkey_rp_id":               passkeySetting.EffectiveRPID(),
		"passkey_rp_ids":              passkeySetting.RelyingPartyIDs(),
		"passkey_allow_insecure":      passkeySetting.AllowInsecureOrigin,
		"passkey_user_verification":   passkeySetting.UserVerification,
		"passkey_attachment":          passkeySetting.AttachmentPreference,
		"setup":                       constant.Setup,
		"user_agreement_enabled":      legalSetting.UserAgreement != "",
		"privacy_policy_enabled":      legalSetting.PrivacyPolicy != "",
		"checkin_enabled":             operation_setting.GetCheckinSetting().Enabled,
	}

	// 根据启用状态注入可选内容
	if cs.ApiInfoEnabled {
		data["api_info"] = console_setting.GetApiInfo()
	}
	if cs.AnnouncementsEnabled {
		data["announcements"] = console_setting.GetAnnouncements()
	}
	if cs.FAQEnabled {
		data["faq"] = console_setting.GetFAQ()
	}

	// Add enabled custom OAuth providers
	customProviders := oauth.GetEnabledCustomProviders()
	if len(customProviders) > 0 {
		type CustomOAuthInfo struct {
			Id                    int    `json:"id"`
			Name                  string `json:"name"`
			Slug                  string `json:"slug"`
			Icon                  string `json:"icon"`
			ClientId              string `json:"client_id"`
			AuthorizationEndpoint string `json:"authorization_endpoint"`
			Scopes                string `json:"scopes"`
		}
		providersInfo := make([]CustomOAuthInfo, 0, len(customProviders))
		for _, p := range customProviders {
			config := p.GetConfig()
			providersInfo = append(providersInfo, CustomOAuthInfo{
				Id:                    config.Id,
				Name:                  config.Name,
				Slug:                  config.Slug,
				Icon:                  config.Icon,
				ClientId:              config.ClientId,
				AuthorizationEndpoint: config.AuthorizationEndpoint,
				Scopes:                config.Scopes,
			})
		}
		data["custom_oauth_providers"] = providersInfo
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    data,
	})
	return
}

func GetNotice(c *gin.Context) {
	common.OptionMapRWMutex.RLock()
	notice := common.OptionMap["Notice"]
	common.OptionMapRWMutex.RUnlock()
	serveRevalidatedJSON(c, notice)
}

func GetAbout(c *gin.Context) {
	common.OptionMapRWMutex.RLock()
	about := common.OptionMap["About"]
	common.OptionMapRWMutex.RUnlock()
	serveRevalidatedJSON(c, about)
}

func GetUserAgreement(c *gin.Context) {
	serveRevalidatedJSON(c, system_setting.GetLegalSettings().UserAgreement)
}

func GetPrivacyPolicy(c *gin.Context) {
	serveRevalidatedJSON(c, system_setting.GetLegalSettings().PrivacyPolicy)
}

func GetMidjourney(c *gin.Context) {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    common.OptionMap["Midjourney"],
	})
	return
}

func GetHomePageContent(c *gin.Context) {
	common.OptionMapRWMutex.RLock()
	homePageContent := common.OptionMap["HomePageContent"]
	common.OptionMapRWMutex.RUnlock()
	serveRevalidatedJSON(c, homePageContent)
}

func SendEmailVerification(c *gin.Context) {
	email, err := service.ValidateAccountEmail(c.Query("email"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if model.IsEmailAlreadyTaken(email) {
		common.ApiError(c, errors.New("邮箱已被使用"))
		return
	}
	if err = model.OmniEmailAbuseAdmit(email, c.ClientIP(), 0, "register"); err != nil {
		common.ApiError(c, err)
		return
	}
	code, err := model.OmniEmailNewCode(false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if err = model.OmniEmailIssue("register", email, 0, code); err != nil {
		common.ApiError(c, err)
		return
	}
	if err = common.SendEmailWithPurpose(common.SystemName+" 邮箱验证", email, "<p>验证码：<strong>"+code+"</strong></p><p>10分钟内有效，请勿泄露。</p>", "register"); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}
func SendPasswordResetEmail(c *gin.Context) {
	email := model.NormalizeEmail(c.Query("email"))
	if common.Validate.Var(email, "required,email") != nil {
		common.ApiError(c, errors.New("邮箱格式无效"))
		return
	}
	if err := model.OmniEmailAbuseAdmit(email, c.ClientIP(), 0, "reset"); err != nil {
		common.ApiError(c, err)
		return
	}
	// A populated Email is not evidence of ownership. Do not infer from usernames.
	if u, e := model.GetUniqueUserByEmail(email); e == nil && model.OmniEmailVerified(*u) && u.Status == common.UserStatusEnabled {
		code, e := model.OmniEmailNewCode(true)
		if e != nil {
			common.ApiError(c, e)
			return
		}
		if e = model.OmniEmailIssue("reset", email, u.Id, code); e != nil {
			common.ApiError(c, e)
			return
		}
		values := url.Values{"email": {email}, "token": {code}}
		link := strings.TrimRight(system_setting.ServerAddress, "/") + "/brand/password-reset.html#" + values.Encode()
		content := "<p>请打开以下链接自行设置新密码：</p><p><a href=\"" + html.EscapeString(link) + "\">重置密码</a></p><p>10分钟内有效；未发起此操作请忽略。</p>"
		if e = common.SendEmailWithPurpose(common.SystemName+" 密码找回", email, content, "reset"); e != nil {
			common.ApiError(c, e)
			return
		}
	}
	common.ApiSuccess(c, gin.H{"message": "如果该邮箱已验证且可找回，重置链接将发送至邮箱。"})
}

type PasswordResetRequest struct {
	Email      string `json:"email"`
	Token      string `json:"token"`
	Password   string `json:"password"`
	RevokeKeys bool   `json:"revoke_keys"`
}

func ResetPassword(c *gin.Context) {
	var req PasswordResetRequest
	if c.ShouldBindJSON(&req) != nil {
		common.ApiError(c, errors.New("请求无效"))
		return
	}
	if err := model.OmniEmailReset(model.NormalizeEmail(req.Email), req.Token, req.Password, req.RevokeKeys); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}
