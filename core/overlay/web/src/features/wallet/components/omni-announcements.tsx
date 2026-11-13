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
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

type Notice = { key: string; title: string; body: string }
export function OmniAnnouncements() {
  const { t } = useTranslation()
  const client = useQueryClient()
  const uid = useAuthStore((state) => state.auth.user?.id)
  const query = useQuery({
    queryKey: ['omni', 'announcements', uid],
    enabled: Boolean(uid),
    queryFn: async () => {
      const r = await api.get('/api/omni/announcements')
      return requireServerSuccess(r.data).data as Notice[]
    },
    staleTime: 60000,
    refetchInterval: 300000,
  })
  const mutation = useMutation({
    mutationFn: async (key: string) => {
      const r = await api.post('/api/omni/notices/read', { key })
      return requireServerSuccess(r.data)
    },
    onSuccess: () =>
      client.invalidateQueries({ queryKey: ['omni', 'announcements'] }),
    onError: (error) => handleServerError(error),
  })
  const notice = query.data?.[0]
  if (!notice) return null
  return (
    <Dialog open>
      <DialogContent showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>{t(notice.title)}</DialogTitle>
          <DialogDescription className='leading-7'>
            {t(notice.body)}
          </DialogDescription>
        </DialogHeader>
        <Button
          disabled={mutation.isPending}
          onClick={() => mutation.mutate(notice.key)}
        >
          {t('知道了')}
        </Button>
      </DialogContent>
    </Dialog>
  )
}
