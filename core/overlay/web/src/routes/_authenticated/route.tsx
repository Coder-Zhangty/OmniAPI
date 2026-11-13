/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { createFileRoute, redirect } from '@tanstack/react-router'

import { GeneralError } from '@/features/errors/general-error'
import { AuthenticatedLayout } from '@/components/layout'
import { resolveAuthentication } from '@/lib/auth-session'
import { useAuthStore } from '@/stores/auth-store'

export const Route = createFileRoute('/_authenticated')({
  beforeLoad: async ({ location }) => {
    // The root guard may have skipped its refresh because no session hint was
    // present. That skip is an optimization for public pages and must not
    // decide a protected route, so resolve against the server before
    // redirecting. An in-memory session returns without a request.
    const outcome = await resolveAuthentication()
    if (outcome.kind === 'transient_error') {
      throw Object.assign(new Error('暂时无法确认登录状态'), { code: 'AUTH_NETWORK_UNCONFIRMED' })
    }

    const { auth } = useAuthStore.getState()

    if (!auth.user || !auth.accessToken) {
      throw redirect({
        to: '/sign-in',
        search: { redirect: location.href },
      })
    }
  },
  component: AuthenticatedLayout,
  errorComponent: ({ error }) => {
    if ((error as Error & { code?: string }).code !== 'AUTH_NETWORK_UNCONFIRMED') return <GeneralError error={error} />
    return <main className='flex min-h-svh items-center justify-center p-6'>
      <div className='max-w-md space-y-5 text-center' role='alert'>
        <h1 className='text-2xl font-semibold'>连接暂时不稳定</h1>
        <p className='text-muted-foreground'>暂时无法确认登录状态，请检查网络后重试。此时无需重新输入账号密码。</p>
        <button type='button' className='rounded-md bg-primary px-5 py-3 text-primary-foreground' onClick={() => window.location.reload()}>重试加载</button>
      </div>
    </main>
  },
})
