"""Install the native administrator route and health bridge with drift detection."""
import argparse
from pathlib import Path
import shutil

def apply(source, frontend_only=False):
    source = Path(source)
    overlay = Path(__file__).parents[1] / 'overlay'
    paths = ['web/src/features/archive/index.tsx', 'web/src/routes/_authenticated/archive/index.tsx']
    if not frontend_only:
        paths.append('controller/omniapi_archive_admin.go')
    for name in paths:
        target = source / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(overlay / name, target)
    sidebar = source / 'web/src/hooks/use-sidebar-data.ts'
    content = sidebar.read_text(encoding='utf-8')
    anchor = "title: t('Admin'),\n        items: ["
    menu = "\n          { title: t('对话封存'), url: '/archive', icon: MessageSquare, requiredRole: ROLE.ADMIN },"
    if "url: '/archive'" not in content:
        if content.count(anchor) != 1:
            raise RuntimeError('Archive sidebar anchor drift')
        sidebar.write_text(content.replace(anchor, anchor + menu), encoding='utf-8')
    if not frontend_only:
        router = source / 'router/api-router.go'
        content = router.read_text(encoding='utf-8')
        line = '\n\tapiRouter.GET("/archive/status", middleware.AdminAuth(), controller.OmniArchiveStatus)'
        if line not in content:
            anchor = '\tapiRouter.GET("/status",'
            if content.count(anchor) != 1:
                raise RuntimeError('Archive administrator API anchor drift')
            position = content.index('\n', content.index(anchor))
            router.write_text(content[:position] + line + content[position:], encoding='utf-8')
    print('Archive workbench source installed')

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', required=True)
    parser.add_argument('--frontend-only', action='store_true')
    args = parser.parse_args()
    apply(args.source, args.frontend_only)
