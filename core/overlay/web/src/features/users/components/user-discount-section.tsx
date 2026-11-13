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
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { SideDrawerSection } from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { handleServerError } from '@/lib/handle-server-error'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { getUserDiscount, saveUserDiscount, type UserDiscountData } from '../user-discount-api'

// This rule has its own versioned save. It never enters the base-user payload.
export function UserDiscountSection({ userId, open }: { userId: number; open: boolean }) {
  const { t } = useTranslation()
  const isRoot = useAuthStore((state) => state.auth.user?.role === ROLE.SUPER_ADMIN)
  const [data, setData] = useState<UserDiscountData | null>(null)
  const [enabled, setEnabled] = useState(false)
  const [margin, setMargin] = useState('40')
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)
  const generation = useRef(0)

  const apply = (next: UserDiscountData) => {
    setData(next)
    setEnabled(next.rule.enabled)
    setMargin(String(Number((next.rule.target_margin * 100).toFixed(8))))
    setConflict(false)
  }

  const load = useCallback(async () => {
    const current = ++generation.current
    setLoading(true)
    setSaving(false)
    setData(null)
    setError('')
    setConflict(false)
    try {
      const next = await getUserDiscount(userId)
      if (current === generation.current) apply(next)
    } catch (cause) {
      if (current === generation.current) {
        setError(t('Failed to load price discount'))
        handleServerError(cause, t('Failed to load price discount'))
      }
    } finally {
      if (current === generation.current) setLoading(false)
    }
  }, [userId, t])

  useEffect(() => {
    if (isRoot && open) void load()
    return () => { generation.current += 1 }
  }, [isRoot, open, load])

  if (!isRoot || !open) return null
  const marginNumber = Number(margin)
  const valid = margin.trim() !== '' && Number.isFinite(marginNumber) && marginNumber >= 0 && marginNumber <= 90
  const legacyBlocked = !!data?.legacy_conflict && enabled
  const disabled = loading || saving || !data || conflict

  const save = async () => {
    if (!data || !valid || disabled || legacyBlocked) return
    const current = generation.current
    setSaving(true)
    setError('')
    try {
      const next = await saveUserDiscount(userId, {
        expected_version: data.version,
        enabled,
        target_margin: marginNumber / 100,
      })
      if (current === generation.current) {
        apply(next)
        toast.success(t('Price discount saved'))
      }
    } catch (cause) {
      if (current !== generation.current) return
      const status = (cause as { response?: { status?: number } }).response?.status
      if (status === 409) {
        setConflict(true)
        setError(t('Price discount changed. Reload before editing again.'))
      } else {
        setError(t('Failed to save price discount'))
        handleServerError(cause, t('Failed to save price discount'))
      }
    } finally {
      if (current === generation.current) setSaving(false)
    }
  }

  return (
    <SideDrawerSection>
      <h3 className='text-sm font-medium'>{t('Price discount')}</h3>
      <p className='text-muted-foreground text-xs'>{t('Applies only to text models paid from the wallet.')}</p>
      <div className='flex items-center gap-2'>
        <Checkbox id={`discount-enabled-${userId}`} checked={enabled} disabled={disabled || (!!data?.legacy_conflict && !enabled)} onCheckedChange={(value) => setEnabled(value === true)} />
        <Label htmlFor={`discount-enabled-${userId}`}>{t('Enable price discount')}</Label>
      </div>
      <div className='space-y-2'>
        <Label htmlFor={`discount-margin-${userId}`}>{t('Target gross margin (%)')}</Label>
        <Input id={`discount-margin-${userId}`} type='number' inputMode='decimal' min={0} max={90} step='0.1' value={margin} disabled={disabled} onChange={(event) => setMargin(event.target.value)} />
        {!valid && <p role='alert' className='text-destructive text-xs'>{t('Enter a margin from 0% to 90%.')}</p>}
      </div>
      {data?.legacy_conflict && <p role='alert' className='text-destructive text-xs'>{t('An existing billing rule conflicts. Resolve it before enabling this discount.')}</p>}
      {error && <p role='alert' className='text-destructive text-xs'>{error}</p>}
      <div className='flex gap-2'>
        <Button type='button' variant='outline' disabled={disabled || !valid || legacyBlocked} onClick={() => void save()}>{saving ? t('Saving...') : t('Save price discount')}</Button>
        {(error || conflict) && <Button type='button' variant='ghost' disabled={loading || saving} onClick={() => void load()}>{t('Reload')}</Button>}
      </div>
    </SideDrawerSection>
  )
}
