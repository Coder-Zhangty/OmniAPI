"""Apply the audited rc.40 commercial overlay; anchor drift fails before writing.

Usage: python core/patches/apply_commercial_v2.py --source PATH [--check]
Run apply_public_id.py first. Overlay sources are versioned in core/overlay.
"""
import argparse
import re
from pathlib import Path


def changes():
    return {
        'go.mod': [('go 1.25.1\n', 'go 1.25.1\n\nrequire github.com/santhosh-tekuri/jsonschema/v6 v6.0.2\n')],
        'middleware/distributor.go': [
            ('\t\t_, pinned, _ := constraints.ResolvedPin()', '\t\tif shouldSelectChannel && modelRequest.Model != "" { if status, reason, _ := model.OmniPriceAvailability(modelRequest.Model); status == "paused" { abortWithOpenAiMessage(c, http.StatusServiceUnavailable, reason); return } }\n\t\t_, pinned, _ := constraints.ResolvedPin()'),
        ],
        'service/channel_select.go': [
            ('\tif retry.GetRetry() == 0 {', '\tif retry.GetRetry() == 0 && !(GetOmniRouting().Enabled && len(GetOmniRouting().Models[modelName]) > 0) {'),
        ],
        'model/pricing.go': [
            ('\tModelName              string', '\tAvailability string `json:"availability,omitempty"`\n\tAvailabilityReason string `json:"availability_reason,omitempty"`\n\tPriceVersion string `json:"price_version,omitempty"`\n\tModelName              string'),
            ('\t\tpricingMap = append(pricingMap, pricing)', '\t\tpricing.Availability, pricing.AvailabilityReason, pricing.PriceVersion = OmniPriceAvailability(model)\n\t\tif pricing.Availability == "paused" { pricing.ModelRatio=0; pricing.ModelPrice=0; pricing.CompletionRatio=0; pricing.BillingExpr=""; pricing.BillingPluginVariants=nil }\n\t\tpricingMap = append(pricingMap, pricing)'),
        ],
        'web/src/routes/pricing/index.tsx': [
            ("    throw redirect({ href: '/brand/models.html?v=omni-v2', replace: true })", "    // Render native Pricing; the retired catalog redirects here."),
        ],
        'web/src/features/pricing/types.ts': [
            ('  model_ratio: number', '  availability?: string\n  availability_reason?: string\n  price_version?: string\n  model_ratio: number'),
        ],
        'web/src/features/pricing/components/model-price-cell.tsx': [
            ('  let metrics: Array<{ label: string; value: string }>', '  if (props.model.availability === "paused") { return <span className="text-muted-foreground">{props.model.availability_reason || t("Price pending verification")}</span> }\n  let metrics: Array<{ label: string; value: string }>'),
        ],
        'web/src/features/pricing/components/model-card.tsx': [
            ('  if (dynamicSummary) {', '  if (props.model.availability === "paused") { priceSummary = <span className="text-muted-foreground col-span-full">{props.model.availability_reason || t("Price pending verification")}</span> } else if (dynamicSummary) {'),
        ],
        'web/src/features/usage-logs/components/timing-metrics-cell.tsx': [
            ("firstTokenSeconds == null ? t('N/A') : formatUseTime(firstTokenSeconds)", "firstTokenSeconds == null ? t('No valid output received') : formatUseTime(firstTokenSeconds)"),
        ],
        'main.go': [('\tperfmetrics.Init()', '\tmodel.StartOmniMaintenance()\n\tperfmetrics.Init()')],
        'model/main.go': [('\terr := DB.AutoMigrate(\n', '\tif err := OmniMigrate(); err != nil { return err }\n\terr := DB.AutoMigrate(\n')],
        'model/user.go': [('_ = inviteUser(inviterId)', 'if err := OmniRegistrationReward(inviterId, user.Id); err != nil { common.SysError("OmniAPI referral: " + err.Error()) }')],
        'controller/subscription.go': [
            ('\t\t"all_subscriptions":  allSubscriptions,', '\t\t"omni_subscription": model.OmniSubscriptionView(userId),\n\t\t"all_subscriptions":  allSubscriptions,'),
        ],
        'service/billing_session.go': [

            ('\tdelta := targetQuota - s.preConsumedQuota', '\tif f,ok:=s.funding.(*OmniFunding);ok && f.flexibleAllowance && f.record!=nil && f.record.Source=="omni_subscription" { return nil }\n\tdelta := targetQuota - s.preConsumedQuota'),
            ('\t\t// TODO: model 层应定义哨兵错误', '\t\tif apiErr, handled := omniFundingError(err); handled { return apiErr }\n\t\t// TODO: model 层应定义哨兵错误'),
            ('\t\t// 1) 退还资金来源', '\t\tlegacyRefundOK:=true\n\t\t// 1) 退还资金来源'),
            ('common.SysLog("error refunding billing source: " + err.Error())', 'legacyRefundOK=false;common.SysLog("error refunding billing source: " + err.Error())'),
            ('common.SysLog("error refunding subscription extra reserved quota: " + err.Error())', 'legacyRefundOK=false;common.SysLog("error refunding subscription extra reserved quota: " + err.Error())'),
            ('\t\t// 2) 退还令牌额度', '\t\tif native,ok:=funding.(*SubscriptionFunding);ok && legacyRefundOK{model.OmniLegacyFinish(native.userId,native.requestId)}\n\t\t// 2) 退还令牌额度'),
            ('\t\tif apiErr := session.preConsume(c, preConsumedQuota); apiErr != nil {', '\t\tif relayInfo.OmniPriceProtected && session.funding.Source() == BillingSourceWallet { session.funding = &OmniFunding{uid:relayInfo.UserId, requestID:relayInfo.RequestId, modelName:""} }\n\t\tif apiErr := session.preConsume(c, preConsumedQuota); apiErr != nil {'),
            ('\tif delta == 0 {', '\tif delta == 0 {\n\t\tif native,ok:=s.funding.(*SubscriptionFunding);ok{if err:=native.Settle(0);err!=nil{return err};s.fundingSettled=true}'),
            ('\tif s.tokenConsumed > 0 {', '\tif f, ok := s.funding.(*OmniFunding); ok && f.reserved > 0 { return true }\n\tif s.tokenConsumed > 0 {'),
            ('\tdelta := actualQuota - s.preConsumedQuota\n', '\tdelta := actualQuota - s.preConsumedQuota\n\tif f, ok := s.funding.(*OmniFunding); ok && !s.fundingSettled {\n\t\tif err := f.Settle(delta); err != nil { return err }; s.fundingSettled = true\n\t}\n'),
            ('\tcase *WalletFunding:\n\t\tif requireAvailableQuota {', '\tcase *OmniFunding:\n\t\treturn funding.Adjust(delta)\n\tcase *WalletFunding:\n\t\tif requireAvailableQuota {'),
            ('\tcase *WalletFunding:\n\t\tif err := model.IncreaseUserQuota', '\tcase *OmniFunding:\n\t\tif err := funding.Adjust(-delta); err != nil { common.SysError(err.Error()) }\n\tcase *WalletFunding:\n\t\tif err := model.IncreaseUserQuota'),
            ('\tpref := common.NormalizeBillingPreference', '\tif relayInfo.OmniBillingActive && preConsumedQuota > 0 { return NewOmniBillingSession(c, relayInfo, preConsumedQuota) }\n\tpref := common.NormalizeBillingPreference'),
            ('\tif s.relayInfo.ForcePreConsume {', '\tif _, ok := s.funding.(*OmniFunding); ok { return false }\n\tif s.relayInfo.ForcePreConsume {'),
        ],
        'service/funding_source.go': [
            ('func (s *SubscriptionFunding) PreConsume(_ int) error {', 'func (s *SubscriptionFunding) PreConsume(_ int) error {\n\tif err:=model.OmniLegacyBegin(s.userId,s.requestId);err!=nil{return err}\n\tsuccess:=false;defer func(){if !success{model.OmniLegacyFinish(s.userId,s.requestId)}}()'),
            ('\ts.subscriptionId = res.UserSubscriptionId', '\tsuccess=true\n\ts.subscriptionId = res.UserSubscriptionId'),
            ('func (s *SubscriptionFunding) Settle(delta int) error {', 'func (s *SubscriptionFunding) Settle(delta int) (err error) {\n\tdefer func(){if err==nil{model.OmniLegacyFinish(s.userId,s.requestId)}}()'),
        ],
        'model/log_format_test.go': [('assert.Equal(t, 77, logs[0].ChannelId)', 'assert.Equal(t, 0, logs[0].ChannelId)')],
        'model/log.go': [
            ('\t\tlogs[i].ChannelName = ""', '\t\tlogs[i].ChannelName = ""\n\t\tlogs[i].ChannelId = 0\n\t\tlogs[i].UpstreamRequestId=""\n\t\tlogs[i].Content=OmniUserMessage(logs[i].Content)\n\t\tlogs[i].Other=OmniPublicLogOther(formatLogOtherJSON(logs[i].Other,logOtherVisibilityUser))'),
        ],
        'model/task.go': [
            ('BillingSource  string', 'OmniRequestID string `json:"omni_request_id,omitempty"`\n\tBillingSource  string'),
        ],
        'controller/relay.go': [
            ('func getChannel(c *gin.Context, info *relaycommon.RelayInfo, retryParam *service.RetryParam) (*model.Channel, *types.NewAPIError) {', 'func getChannel(c *gin.Context, info *relaycommon.RelayInfo, retryParam *service.RetryParam) (*model.Channel, *types.NewAPIError) {\n\tif selected, managed, err := omniPrimary(c, info); managed { return selected, err }'),
            ('newAPIError.SetMessage(common.MessageWithRequestId(newAPIError.Error(), requestId))','newAPIError.SetMessage(common.MessageWithRequestId(model.OmniUserMessage(newAPIError.Error()), requestId))'),
            ('\tdiagnostics.insertStart(task)', '\tservice.OmniBindTask(relayInfo, task)\n\tdiagnostics.insertStart(task)'),
        ],
        'relay/relay_task.go': [
            ('\t// 7. 预扣费', '\tif active, err := model.OmniActive(info.UserId); err != nil {return nil, service.TaskErrorWrapperLocal(err,"billing_unavailable",http.StatusServiceUnavailable)} else if active {info.OmniBillingActive=true}\n\t// 7. 预扣费'),
        ],
        'service/task_billing.go': [
            ('\tquota := task.Quota', '\tif task.PrivateData.OmniRequestID != "" { return OmniFinishTask(ctx,task,0,reason) }\n\tquota := task.Quota'),
            ('\tpreConsumedQuota := task.Quota', '\tif task.PrivateData.OmniRequestID != "" { OmniFinishTask(ctx,task,actualQuota,reason); return }\n\tpreConsumedQuota := task.Quota'),
        ],
        'service/task_polling.go': [
            ('\tperfmetrics.RecordTaskResult(task, taskResult)', '\tdefer func(){if task.PrivateData.OmniRequestID!="" { if task.Status==model.TaskStatusFailure {OmniFinishTask(ctx,task,0,task.FailReason)} else if task.Status==model.TaskStatusSuccess {OmniFinishTask(ctx,task,task.Quota,"任务完成")} }}()\n\tperfmetrics.RecordTaskResult(task, taskResult)'),
        ],
        'service/log_info_generate.go': [
            ('\tother.SetAdmin("use_channel", ctx.GetStringSlice("use_channel"))', '\tOmniAppendRouteLog(ctx, relayInfo, other)'),
            ('\t// billing_source: "wallet" or "subscription"', '\tother.SetPublic("price_version", relayInfo.OmniPriceVersion)\n\t// billing_source: "wallet" or "subscription"'),
            ('\t// billing_source: "wallet" or "subscription"', '\tif r, err := model.OmniRequestRecord(relayInfo.RequestId); err == nil {\n\t\tother.SetPublic("billing_version", 2)\n\t\tother.SetPublic("subscription_start", r.SubscriptionStart)\n\t\tother.SetPublic("five_hour_window", r.FiveEpoch)\n\t\tother.SetPublic("week_window", r.WeekEpoch)\n\t}\n\tother.SetAdmin("channel_attempts", model.OmniPublicAttempts(relayInfo.RequestId))\n\tother.SetAdmin("channel_costs", model.OmniRequestAttempts(relayInfo.RequestId))\n\t// billing_source: "wallet" or "subscription"'),
        ],
        'relay/common/relay_info.go': [('\tForcePreConsume bool', '\tForcePreConsume bool\n\tOmniBillingActive bool')],
        'relay/request_billing.go': [
            ('func PrepareRequestBilling(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {', 'func PrepareRequestBilling(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {\n\tif status, reason, version := model.OmniPriceAvailability(info.OriginModelName); status == "paused" { return types.NewError(errors.New(reason), types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusServiceUnavailable), types.ErrOptionWithSkipRetry()) } else if status == "available" { info.OmniPriceVersion=version; info.OmniPriceProtected=true; info.ForcePreConsume=true; service.OmniEnsureOutputLimit(info); if err := service.OmniValidatePricedRequest(info); err != nil { return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithStatusCode(http.StatusBadRequest), types.ErrOptionWithSkipRetry()) } }'),
            ('"github.com/QuantumNous/new-api/logger"', '"github.com/QuantumNous/new-api/logger"\n "github.com/QuantumNous/new-api/model"'),
            ('\tneedSensitiveCheck := setting.ShouldCheckPromptSensitive()', '\tomniActive, omniErr := model.OmniActive(info.UserId)\n\tif omniErr != nil { return types.NewError(omniErr, types.ErrorCodeQueryDataError) }\n\tif omniActive {\n\t\tinfo.ForcePreConsume = true\n\t\tinfo.OmniBillingActive = true\n\t\tif err := service.OmniValidateOutputLimits(info); err != nil { return types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest)) }\n\t\tservice.OmniEnsureOutputLimit(info)\n\t}\n\tneedSensitiveCheck := setting.ShouldCheckPromptSensitive()'),
            ('(needSensitiveCheck || constant.CountToken)', '(info.OmniPriceProtected || omniActive || needSensitiveCheck || constant.CountToken)'),
        ],
        'relay/helper/price.go': [

            ('float64(promptTokens) * preConsumeMultiplier', 'float64(service.OmniReservedInputTokens(info, promptTokens)) * service.OmniReserveMultiplier(info, preConsumeMultiplier)'),
            ('"github.com/QuantumNous/new-api/logger"', '"github.com/QuantumNous/new-api/logger"\n "github.com/QuantumNous/new-api/service"'),
            ('quotaBeforeGroup *= preConsumeMultiplier', 'quotaBeforeGroup *= service.OmniReserveMultiplier(info, preConsumeMultiplier)'),
            ('\t\tC:   0,', '\t\tC:   float64(service.OmniReservedOutput(info)),'),
            ('\t\tP:   float64(promptTokens),', '\t\tP:   float64(service.OmniReservedInputTokens(info, promptTokens)) * service.OmniInputReserveFactor(info),'),
            ('\t\tLen: float64(promptTokens),', '\t\tLen: float64(service.OmniReservedInputTokens(info, promptTokens)),'),
            ('\t\tratio := modelRatio * groupRatioInfo.GroupRatio', '\t\tpreConsumedTokens += float64(service.OmniReservedOutput(info)) * completionRatio\n\t\tratio := modelRatio * groupRatioInfo.GroupRatio'),
        ],
        'relay/channel/api_request.go': [
            ('http.NewRequest(c.Request.Method, fullRequestURL, requestBody)', 'http.NewRequestWithContext(c.Request.Context(), c.Request.Method, fullRequestURL, requestBody)'),
        ],
        'relay/channel/openai/adaptor.go': [
            ('return channel.DoApiRequest(a, c, info, requestBody)', 'return a.omniRequest(c, info, requestBody)'),
        ],
        'relay/channel/openai/relay-openai.go': [
            ('\tHandleFinalResponse(c, info, lastStreamData, responseId, createAt, model, systemFingerprint, usage, containStreamUsage)', '\tif _, managed := resp.Body.(*omniBody); managed && info.StreamStatus.EndReason != relaycommon.StreamEndReasonDone {\n\t\tomniStreamInterrupted(c, info, lastStreamData)\n\t\treturn usage, nil\n\t}\n\tHandleFinalResponse(c, info, lastStreamData, responseId, createAt, model, systemFingerprint, usage, containStreamUsage)'),
        ],
        'web/src/hooks/use-sidebar-data.ts': [
            ("title: t('Personal'),\n        items: [", "title: t('Personal'),\n        items: [\n          { title: t('联系客服'), url: '/brand/support.html', icon: MessageSquare },\n          { title: t('充值记录与核验状态'), url: '/brand/topup-status.html', icon: ClipboardList },"),
            ("title: t('Admin'),\n        items: [", "title: t('Admin'),\n        items: [\n          { title: t('客服工作台'), url: '/brand/support-admin.html', icon: MessageSquare },\n          { title: t('充值核验'), url: '/brand/admin.html', icon: ClipboardList },\n          { title: t('订阅与会员管理'), url: '/brand/commerce-admin.html', icon: CreditCard },"),
        ],
        'web/src/components/layout/components/nav-group.tsx': [
            ('to={item.url}\n            preload=', 'to={item.url}\n            reloadDocument={String(item.url).startsWith(\'/brand/\')}\n            preload='),
        ],
        'web/src/components/layout/components/authenticated-layout.tsx': [
            ("import { AppHeader }", "import { OmniAnnouncements } from '@/features/wallet/components/omni-announcements'\nimport { AppHeader }"),
            ('          <AppHeader />', '          <AppHeader />\n          <OmniAnnouncements />'),
        ],
        'web/src/components/layout/components/public-layout.tsx': [
            ("import type { TopNavLink }", "import { OmniAnnouncements } from '@/features/wallet/components/omni-announcements'\nimport type { TopNavLink }"),
            ('      {props.showMainContainer !== false ?', '      <OmniAnnouncements />\n      {props.showMainContainer !== false ?'),
        ],
        'web/src/features/wallet/index.tsx': [
            ("import { AffiliateRewardsCard }", "import { OmniWallet } from './components/omni-wallet'\nimport { AffiliateRewardsCard }"),
            ("            <AffiliateRewardsCard\n              user={user}\n              affiliateLink={affiliateLink}\n              onTransfer={() => setTransferDialogOpen(true)}\n              complianceConfirmed={\n                topupInfo?.payment_compliance_confirmed !== false\n              }\n              loading={affiliateLoading}\n            />", ''),
            ('            <WalletStatsCard user={user} loading={userLoading} />', '            <AffiliateRewardsCard user={user} affiliateLink={affiliateLink} onTransfer={() => setTransferDialogOpen(true)} complianceConfirmed={topupInfo?.payment_compliance_confirmed !== false} loading={affiliateLoading} />\n            <OmniWallet />\n            <WalletStatsCard user={user} loading={userLoading} />'),
        ],
        'web/src/features/wallet/components/affiliate-rewards-card.tsx': [
            ("import { useTranslation } from 'react-i18next'", "import { useTranslation } from 'react-i18next'\nimport { useQuery } from '@tanstack/react-query'\nimport { api } from '@/lib/api'\nimport { requireServerSuccess } from '@/lib/server-error-message'"),
            ("  const { t } = useTranslation()", "  const { t } = useTranslation()\n  const { data: commercial } = useQuery({queryKey:['omni','catalog'],queryFn:async()=>requireServerSuccess((await api.get('/api/omni/catalog')).data).data as {referrals_enabled:boolean},staleTime:60000})\n  const directCredits = commercial?.referrals_enabled === true"),
            ('const hasRewards = (user?.aff_quota ?? 0) > 0', 'const hasRewards = !directCredits && (user?.aff_quota ?? 0) > 0'),
            ("[t('Pending'), formatQuota(user?.aff_quota ?? 0)]", "[t('邀请码'), affiliateLink.split('aff=')[1]?.split('&')[0] || '—']"),
            ("'Earn rewards when users join through your referral link. Transfer accumulated rewards to your balance anytime.'", "directCredits ? '成功邀请新用户注册即可获得18元等值额度，直接入账并通知，无需手动转入。' : 'Earn rewards when users join through your referral link. Transfer accumulated rewards to your balance anytime.'"),
        ],
        'router/api-router.go': [
            ('\t\tapiRouter.GET("/setup",', '\t\tomni := apiRouter.Group("/omni")\n\t\tomni.GET("/catalog", controller.OmniCatalog)\n\t\tomni.GET("/announcements", middleware.UserAuth(), controller.OmniAnnouncements)\n\t\tomni.GET("/self", middleware.UserAuth(), controller.OmniSelf)\n\t\tomni.POST("/preferences", middleware.UserAuth(), controller.OmniPreference)\n\t\tomni.POST("/cards/redeem", middleware.UserAuth(), controller.OmniRedeemCard)\n\t\tomni.POST("/cards/claim", middleware.UserAuth(), controller.OmniClaimBenefit)\n\t\tomni.POST("/notices/read", middleware.UserAuth(), controller.OmniReadNotice)\n\t\tomni.GET("/orders", middleware.UserAuth(), controller.OmniOrders)\n\t\tomni.POST("/purchase", middleware.UserAuth(), middleware.UserCriticalRateLimit("commerce"), controller.OmniPurchaseWallet)\n\t\tomni.POST("/orders", middleware.UserAuth(), middleware.UserCriticalRateLimit("commerce"), controller.OmniCreateOrder)\n\t\tomni.POST("/orders/paid", middleware.UserAuth(), controller.OmniPaidOrder)\n\t\tomni.GET("/admin/orders", middleware.AdminAuth(), controller.OmniOrders)\n\t\tomni.POST("/admin/credit", middleware.AdminAuth(), controller.OmniVerifiedCredit)\n\t\tomni.POST("/admin/refund", middleware.AdminAuth(), controller.OmniRefundReceipt)\n\t\tomni.POST("/admin/activate", middleware.AdminAuth(), controller.OmniActivateSubscription)\n\t\tomni.POST("/admin/cards", middleware.AdminAuth(), controller.OmniGrantReset)\n\t\tomni.POST("/admin/cards/revoke", middleware.AdminAuth(), controller.OmniRevokeReset)\n\t\tapiRouter.GET("/setup",'),
        ],
    }


_base_changes = changes

def changes():
    edits = _base_changes()
    extra = {
      'middleware/distributor.go': [
        ('if shouldSelectChannel && modelRequest.Model != "" {', 'omniNormalizeModel(c, modelRequest)\n\t\tif shouldSelectChannel && modelRequest.Model != "" {'),
        ('func TokenModelLimitAllows(limit map[string]bool, model string) bool {', 'func TokenModelLimitAllows(limit map[string]bool, model string) bool {\n\tlimit, model = omniCanonicalLimits(limit, model)'),
      ],
      'middleware/auth.go': [('c.Set("token_model_limit", token.GetModelLimitsMap())', 'c.Set("token_model_limit", model.OmniCanonicalLimit(token.GetModelLimitsMap()))')],
      'controller/model.go': [
        ('models := service.GetGroupsEnabledModels(ownerGroups)', 'models := model.OmniCanonicalModels(service.GetGroupsEnabledModels(ownerGroups), true)'),
        ('"data":    model.GetEnabledModels(),', '"data":    model.OmniCanonicalModels(model.GetEnabledModels(), true),'),
        ('modelId := c.Param("model")', 'modelId := model.OmniCanonicalModel(c.Param("model"))'),
        ('if aiModel, ok := openAIModelsMap[modelId]; ok {', 'aiModel := buildOpenAIModel(modelId, nil)\n\tgroups, groupErr := getModelListGroups(c)\n\tallowed := false\n\tif groupErr == nil { for _, name := range model.OmniCanonicalModels(service.GetGroupsEnabledModels(groups.ownerGroups), true) { if name == modelId { allowed = true; break } } }\n\tif common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled) { value, _ := common.GetContextKey(c, constant.ContextKeyTokenModelLimit); limits, _ := value.(map[string]bool); allowed = allowed && middleware.TokenModelLimitAllows(limits, modelId) }\n\tif allowed {'),
      ],
      'controller/pricing.go': [('pricing := model.GetPricing()', 'pricing := model.OmniPublicPricing(model.GetPricing())')],
       'controller/channel-test.go': [
        ('func TestChannel(c *gin.Context) {', 'func TestChannel(c *gin.Context) {\n\tif OmniDiagnosticChannelTest(c) { return }'),
        ('\tsummary.Tested++', '\tif result.localErr != nil { return summary }\n\tsummary.Tested++'),
      ],
      'relay/common/relay_info.go': [
        ('Request:         request,', 'Request:         request,\n\t\tOmniRegistryVersion: c.GetString("omni_registry_version"),'),
        
      ],
       'relay/helper/price.go': [
        ('func HasModelBillingConfig(modelName string) bool {', 'func HasModelBillingConfig(modelName string) bool {\n\tmodelName = model.OmniCanonicalModel(modelName)'),
        ('if billing_setting.GetBillingMode(billingModelName) == billing_setting.BillingModeTieredExpr {', 'if info.OmniPriceExpression != "" || billing_setting.GetBillingMode(billingModelName) == billing_setting.BillingModeTieredExpr {'),
        ('exprStr, ok := billing_setting.GetBillingExpr(billingModelName)', 'exprStr, ok := billing_setting.GetBillingExpr(billingModelName)\n\tif info.OmniPriceExpression != "" { exprStr, ok = info.OmniPriceExpression, true }'),
      ],
    }
    for name, rows in extra.items():
        edits.setdefault(name, []).extend(rows)
    return edits


def apply(source, check=False):
    source = Path(source)
    overlay = Path(__file__).resolve().parents[1] / 'overlay'
    staged = {}
    for name, edits in changes().items():
        path = source / name
        content = path.read_text(encoding='utf-8')
        if name == 'main.go':
            content = re.sub(r'\n[ \t]*model\.StartOmniMaintenance\(\)', '', content)
        if name == 'relay/request_billing.go':
            content = content.replace('(omniActive || needSensitiveCheck || constant.CountToken)', '(needSensitiveCheck || constant.CountToken)')
            signature='func PrepareRequestBilling(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {'
            if signature in content and 'omniActive, omniErr := model.OmniActive(info.UserId)' in content:
                start=content.index(signature)+len(signature)
                end=content.index('omniActive, omniErr := model.OmniActive(info.UserId)',start)
                prefix=content[start:end]
                if 'OmniPriceAvailability' in prefix or 'omniRequestPrice' in prefix:
                    content=content[:start]+'\n\t'+content[end:]
        for before, after in edits:
            if name == 'router/api-router.go':
                after = after.replace('omni.GET("/catalog", controller.OmniCatalog)\n', 'omni.GET("/catalog", controller.OmniCatalog)\n\t\tomni.GET("/pricing", controller.OmniPricing)\n\t\tomni.GET("/self/catalog", middleware.UserAuth(), controller.OmniCatalog)\n')
                after = after.replace('omni.POST("/admin/credit",', 'omni.GET("/admin/model-registry", middleware.AdminAuth(), controller.OmniRegistryAdmin)\n\t\tomni.PUT("/admin/model-registry", middleware.AdminAuth(), controller.OmniRegistryAliases)\n\t\tomni.GET("/admin/routing", middleware.AdminAuth(), controller.OmniRoutingAdmin)\n\t\tomni.POST("/admin/routing", middleware.AdminAuth(), controller.OmniRoutingUpdate)\n\t\tomni.POST("/admin/credit",')
                after = after.replace('omni.POST("/admin/credit",', 'omni.POST("/admin/import-receipt", middleware.AdminAuth(), controller.OmniImportReceipt)\n\t\tomni.POST("/admin/credit",')
                after = after.replace('omni.POST("/admin/credit",', 'omni.POST("/admin/referrals/enable", middleware.AdminAuth(), controller.OmniEnableReferrals)\n\t\tomni.POST("/admin/credit",')
            expected = 2 if before == '_ = inviteUser(inviterId)' else 1
            if before.startswith('http.NewRequest('):
                expected = 2
            if name == 'router/api-router.go' and '\t\tomni := apiRouter.Group("/omni")' in content:
                start = content.index('\t\tomni := apiRouter.Group("/omni")')
                match = re.search(r'(?m)^\s*omni\.POST\("/admin/cards/revoke"[^\n]*\n', content[start:])
                if not match:raise RuntimeError('Omni router block drift')
                end = start + match.end()
                content = content[:start] + after[:-len(before)] + content[end:]
                continue
            if re.sub(r'[\s;]+', '', after) in re.sub(r'[\s;]+', '', content):
                continue
            if content.count(before) != expected:
                raise RuntimeError(f'Anchor drift in {name}: {before[:65]!r}')
            content = content.replace(before, after)
        if name == 'relay/request_billing.go':
            content = content.replace('model.OmniPriceAvailability(info.OriginModelName)', 'omniRequestPrice(c, info)')
        if name == 'controller/model.go' and '"github.com/QuantumNous/new-api/middleware"' not in content:
            content=content.replace('import (','import (\n "github.com/QuantumNous/new-api/middleware"',1)
        staged[path] = content
    for path in overlay.rglob('*'):
        if path.is_file():
            target = source / path.relative_to(overlay)
            staged[target] = path.read_text(encoding='utf-8')
    info_path = source / 'relay/common/relay_info.go'
    sum_path = source / 'go.sum'
    sums = sum_path.read_text(encoding='utf-8')
    for entry in ['github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 h1:KRzFb2m7YtdldCEkzs6KqmJw4nqEVZGK7IN2kJkjTuQ=', 'github.com/santhosh-tekuri/jsonschema/v6 v6.0.2/go.mod h1:JXeL+ps8p7/KNMjDQk3TCwPpBy0wYklyWTfbkIzdIFU=']:
        if entry not in sums:sums = sums.rstrip() + '\n' + entry + '\n'
    staged[sum_path] = sums
    if 'OmniPriceProtected' not in staged[info_path]:
        staged[info_path] = re.sub(r'(OmniBillingActive\s+bool)', r'\1\n\tOmniPriceProtected bool\n\tOmniPriceVersion string', staged[info_path], count=1)
    if 'OmniPriceExpression string' not in re.sub(r'[ \t]+', ' ', staged[info_path]):
        staged[info_path] = re.sub(r'(OmniBillingActive\s+bool)', r'\1\n\tOmniPriceExpression string\n\tOmniPriceQuote []byte', staged[info_path], count=1)
    if 'OmniRegistryVersion string' not in re.sub(r'[ \t]+', ' ', staged[info_path]):
        staged[info_path] = re.sub(r'(OmniBillingActive\s+bool)', r'\1\n\tOmniRegistryVersion string', staged[info_path], count=1)
    relay_path = source / 'controller/relay.go'
    if 'if selected, managed, err := omniPrimary(c, info); managed { return selected, err }' not in staged[relay_path]:
        raise RuntimeError('Omni primary routing entrypoint is not connected')
    import json
    for path in (source / 'web/src/i18n/locales').glob('*.json'):
        data = json.loads(path.read_text(encoding='utf-8'))
        chinese = path.stem in ('zh', 'zh-TW')
        data['Price pending verification'] = '价格待核验' if chinese else 'Price pending verification'
        data['No valid output received'] = '未收到有效输出' if chinese else 'No valid output received'
        staged[path] = json.dumps(data, ensure_ascii=False, indent=2) + '\n'
    if not check:
        for path, content in staged.items():
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content, encoding='utf-8', newline='\n')
        import runpy
        runpy.run_path(str(Path(__file__).with_name('apply_remediation_20261016.py')))['apply'](source)
        runpy.run_path(str(Path(__file__).with_name('apply_private_billing_20261017.py')))['apply'](source)
        runpy.run_path(str(Path(__file__).with_name('apply_claude_tool_media_20261019.py')))['apply'](source)
        runpy.run_path(str(Path(__file__).with_name('apply_route_precommit_20261019.py')))['apply'](source)
        runpy.run_path(str(Path(__file__).with_name('apply_user_discounts_20261019.py')))['apply'](source)
        runpy.run_path(str(Path(__file__).with_name('apply_codex_web_search_20261021.py')))['apply'](source)
    print(('CHECK' if check else 'APPLIED'), len(staged), 'commercial v2 source files')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', required=True)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    apply(args.source, args.check)
