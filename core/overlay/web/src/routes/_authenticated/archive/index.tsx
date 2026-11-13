import { createFileRoute, redirect } from '@tanstack/react-router'

import { ArchiveWorkspace } from '@/features/archive'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

export const Route = createFileRoute('/_authenticated/archive/')({
  beforeLoad: () => {
    const { auth } = useAuthStore.getState()
    if (!auth.user || auth.user.role < ROLE.ADMIN) {
      throw redirect({ to: '/403' })
    }
  },
  component: ArchiveWorkspace,
})
