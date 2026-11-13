import type { ComponentType, PropsWithChildren, ReactElement, ReactNode } from 'react'
import type { SectionPageLayout } from '@/components/layout'

export function GiftCodesPanel(): ReactElement
export function IntegratedRedemptions(props: {
  Provider: ComponentType<PropsWithChildren>
  Layout: typeof SectionPageLayout
  PrimaryButtons: ComponentType
  Table: ComponentType
  Dialogs: ComponentType
  title: ReactNode
}): ReactElement
