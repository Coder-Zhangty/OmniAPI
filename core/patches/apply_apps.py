"""Apply native-app overlay after the existing commercial overlay; idempotent."""
import argparse
import shutil
from pathlib import Path

def apply(root):
    overlay=Path(__file__).resolve().parents[1]/'overlay'
    for relative in ['model/omniapi_app.go','model/omniapi_app_test.go','controller/omniapi_app.go','controller/omniapi_app_test.go']:
        shutil.copy2(overlay/relative,root/relative)
    migrations=root/'model/omniapi.go'
    text=migrations.read_text(encoding='utf-8')
    old='DB.AutoMigrate(&OmniLegacyPause{}'
    new='DB.AutoMigrate(&OmniAppLogin{}, &OmniAppChildSession{}, &OmniSyncSubscription{}, &OmniLegacyPause{}'
    previous='DB.AutoMigrate(&OmniAppLogin{}, &OmniSyncSubscription{}, &OmniLegacyPause{}'
    if previous in text:text=text.replace(previous,new)
    if '&OmniAppChildSession{}' not in text:
        if text.count(old)!=1:raise ValueError('App migration anchor drift')
        text=text.replace(old,new)
    migrations.write_text(text,encoding='utf-8')
    session=root/'service/auth_session.go'
    text=session.read_text(encoding='utf-8')
    anchor='func ValidateLoginSession(identity AuthIdentity) (*model.UserSession, *model.UserBase, error) {'
    changed=anchor+'\n\t// Parent session is checked below only for native WebViews.'
    initial=anchor+'\n\tif err := model.OmniAppParentSessionActive(identity); err != nil { return nil, nil, ErrLoginSessionRevoked }'
    text=text.replace(initial,anchor)
    if changed not in text:
        if text.count(anchor)!=1:raise ValueError('Native parent-session anchor drift')
        session.write_text(text.replace(anchor,changed),encoding='utf-8')
    text=session.read_text(encoding='utf-8')
    refresh_anchor='\tuserCache, err := model.GetUserCache(session.UserID)'
    refresh_guard='\tif session.LoginMethod == "native:webview" { if err := model.OmniAppParentSessionActive(AuthIdentity{UserID:session.UserID,SessionID:session.SID,UserAuthVersion:session.UserAuthVersion,SessionVersion:session.Version}); err != nil { return nil,nil,ErrLoginSessionRevoked } }\n'+refresh_anchor
    start=text.index('func RefreshLoginSession(')
    if refresh_guard not in text[start:]:
        before,after=text[:start],text[start:]
        if after.count(refresh_anchor)!=1:raise ValueError('Native WebView refresh anchor drift')
        session.write_text(before+after.replace(refresh_anchor,refresh_guard),encoding='utf-8')
    text=session.read_text(encoding='utf-8')
    anchor='\treturn session, user, nil\n}\n\n// ValidateSessionReference'
    changed='\tif session.LoginMethod == "native:webview" { if err := model.OmniAppParentSessionActive(identity); err != nil { return nil, nil, ErrLoginSessionRevoked } }\n'+anchor
    if changed not in text:
        if text.count(anchor)!=1:raise ValueError('Native child-session guard anchor drift')
        session.write_text(text.replace(anchor,changed),encoding='utf-8')
    route=root/'router/api-router.go'
    text=route.read_text(encoding='utf-8')
    routes='''
        // OmniAPI native apps: session-bound approval and separate sync product.
        apiRouter.POST("/app/login/start", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.OmniAppStart)
        apiRouter.POST("/app/login/approve", middleware.UserAuth(), middleware.UserCriticalRateLimit("app-login"), controller.OmniAppApprove)
        apiRouter.POST("/app/login/exchange", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.OmniAppExchange)
        apiRouter.POST("/app/web-session/exchange", middleware.SessionCookieOriginGuard(), middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.OmniAppWebExchange)
        apiRouter.POST("/app/login/refresh", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.OmniAppRefresh)
        apiRouter.POST("/app/login/logout", middleware.UserAuth(), controller.OmniAppLogout)
        apiRouter.GET("/app/sync/self", middleware.UserAuth(), controller.OmniAppSyncSelf)
        apiRouter.POST("/app/sync/token", middleware.UserAuth(), middleware.UserCriticalRateLimit("app-sync"), controller.OmniAppSyncToken)
        apiRouter.POST("/app/sync/purchase", middleware.UserAuth(), middleware.UserCriticalRateLimit("commerce"), controller.OmniAppSyncPurchase)
        apiRouter.POST("/app/sync/introspect", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.OmniAppSyncIntrospect)
        apiRouter.GET("/app/admin/sync-policy", middleware.AdminAuth(), controller.OmniAppSyncPolicy)
        apiRouter.POST("/app/admin/sync-policy", middleware.AdminAuth(), controller.OmniAppSyncPolicy)
'''
    if 'controller.OmniAppStart' not in text:
        if 'controller.OmniNativeWebExchange' in text:
            routes='\n'.join(line for line in routes.split('\n') if '/app/web-session/exchange' not in line)
        anchor='apiRouter.GET("/setup", controller.GetSetup)'
        if text.count(anchor)!=1:raise ValueError('App route anchor drift')
        route.write_text(text.replace(anchor,routes+anchor),encoding='utf-8')
    else:
        for name,line in [
          ('OmniAppWebExchange','        apiRouter.POST("/app/web-session/exchange", middleware.SessionCookieOriginGuard(), middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.OmniAppWebExchange)'),
          ('OmniAppLogout','        apiRouter.POST("/app/login/logout", middleware.UserAuth(), controller.OmniAppLogout)')]:
            if 'controller.'+name not in text and not (name=='OmniAppWebExchange' and 'controller.OmniNativeWebExchange' in text):
                anchor='apiRouter.GET("/setup", controller.GetSetup)'
                if text.count(anchor)!=1:raise ValueError('App route upgrade anchor drift')
                text=text.replace(anchor,line+'\n'+anchor)
        route.write_text(text,encoding='utf-8')

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--source',type=Path,required=True)
    apply(p.parse_args().source)
