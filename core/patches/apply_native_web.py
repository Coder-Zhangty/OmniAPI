"""Small native WebView bridge overlay for the currently deployed gateway."""
from pathlib import Path
import argparse
import shutil

def apply(root):
    overlay=Path(__file__).resolve().parents[1]/'overlay'
    for name in ['model/omni_native_web.go','model/omni_native_web_test.go','service/omni_native_web.go','service/omni_native_web_test.go','controller/omni_native_web.go']:
        shutil.copy2(overlay/name,root/name)
    def patch(name,old,new):
        path=root/name;text=path.read_text(encoding='utf-8')
        if new in text:return
        if text.count(old)!=1:raise ValueError('Native web anchor drift: '+name)
        path.write_text(text.replace(old,new),encoding='utf-8')
    patch('model/omniapi.go','return DB.AutoMigrate(', 'return DB.AutoMigrate(&OmniNativeWebSession{}, ')
    route=root/'router/api-router.go'
    route_text=route.read_text(encoding='utf-8')
    if 'controller.OmniAppWebExchange)' in route_text:
        route.write_text(route_text.replace('controller.OmniAppWebExchange)', 'controller.OmniNativeWebExchange)'),encoding='utf-8')
    if 'controller.OmniNativeWebExchange)' not in route.read_text(encoding='utf-8'):
        patch('router/api-router.go','apiRouter.GET("/setup", controller.GetSetup)',
              'apiRouter.POST("/app/web-session/exchange", middleware.SessionCookieOriginGuard(), middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.OmniNativeWebExchange)\n        apiRouter.GET("/setup", controller.GetSetup)')
    route_text=route.read_text(encoding='utf-8');seen=False;lines=[]
    for line in route_text.splitlines(keepends=True):
        if 'apiRouter.POST("/app/web-session/exchange"' in line:
            if seen:continue
            seen=True
        lines.append(line)
    route.write_text(''.join(lines),encoding='utf-8')
    patch('service/auth_session.go','\treturn session, user, nil\n}\n\n// ValidateSessionReference',
          '\tif err := ValidateNativeWebSession(session); err != nil { return nil,nil,err }\n\treturn session, user, nil\n}\n\n// ValidateSessionReference')
    patch('service/auth_session.go','\tuserCache, err := model.GetUserCache(session.UserID)',
          '\tif err := ValidateNativeWebSession(session); err != nil { return nil,nil,err }\n\tuserCache, err := model.GetUserCache(session.UserID)')

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--source',type=Path,required=True);apply(p.parse_args().source)
