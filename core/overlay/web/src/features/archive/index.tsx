import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { refreshAuthentication } from '@/lib/auth-session'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

const ARCHIVE_PATH = '/archive-v3/'
const ARCHIVE_ORIGIN = 'https://omniapi.biz'

export function ArchiveWorkspace() {
  const { t } = useTranslation()
  const [attempt, setAttempt] = useState(0)
  const [session, setSession] = useState({ ready: false, error: '' })
  const canonical = window.location.hostname !== 'omniapi.hyeroomy.com'

  useEffect(() => {
    if (!canonical) return
    const controller = new AbortController()
    setSession({ ready: false, error: '' })
    const createArchiveSession = async (accessToken: string) =>
      fetch(ARCHIVE_PATH + 'api/session', {
        method: 'POST',
        credentials: 'same-origin',
        headers: {
          'Content-Type': 'application/json',
          'X-Archive-Request': '1',
        },
        body: JSON.stringify({ token: accessToken }),
        signal: controller.signal,
      })

    async function connect() {
      let { auth } = useAuthStore.getState()
      if (!auth.accessToken) {
        await refreshAuthentication()
        auth = useAuthStore.getState().auth
      }
      if (!auth.accessToken || !auth.user || auth.user.role < ROLE.ADMIN) {
        throw new Error('请重新登录控制台管理员账号。')
      }
      let response = await createArchiveSession(auth.accessToken)
      if (response.status === 401 && !controller.signal.aborted) {
        await refreshAuthentication()
        const freshToken = useAuthStore.getState().auth.accessToken
        if (freshToken && freshToken !== auth.accessToken) {
          response = await createArchiveSession(freshToken)
        }
      }
      if (!response.ok) {
        const error = await response.json().catch(() => null)
        throw new Error(error?.error || '暂时无法打开封存工作台，请重试。')
      }
      if (!controller.signal.aborted) setSession({ ready: true, error: '' })
    }

    void connect().catch((error: unknown) => {
      if (!controller.signal.aborted) {
        setSession({
          ready: false,
          error: error instanceof Error ? error.message : '封存工作台加载失败。',
        })
      }
    })
    return () => controller.abort()
  }, [attempt, canonical])

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('对话封存')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          variant='outline'
          onClick={() =>
            window.open(
              canonical ? ARCHIVE_PATH : ARCHIVE_ORIGIN + '/archive',
              '_blank',
              'noopener,noreferrer'
            )
          }
        >
          {t(canonical ? '打开独立工作台' : '前往主站工作台')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        {!canonical ? (
          <p className='text-muted-foreground p-4 text-sm'>
            {t('请从主站打开对话封存。')}
          </p>
        ) : session.ready ? (
          <iframe
            src={ARCHIVE_PATH + 'embed'}
            title={t('管理员对话封存工作台')}
            className='block h-full w-full rounded-lg border'
            referrerPolicy='no-referrer'
            allow='clipboard-write'
          />
        ) : session.error ? (
          <div role='alert' className='flex items-center gap-3 p-4 text-sm'>
            <span className='text-destructive'>{session.error}</span>
            <Button variant='outline' onClick={() => setAttempt((n) => n + 1)}>
              {t('重试')}
            </Button>
          </div>
        ) : (
          <p role='status' className='text-muted-foreground p-4 text-sm'>
            {t('正在打开封存工作台…')}
          </p>
        )}
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

