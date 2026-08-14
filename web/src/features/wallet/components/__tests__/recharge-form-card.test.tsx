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
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'
import type React from 'react'

const domWindow = new Window()
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

const { RechargeFormCard } = await import('../recharge-form-card')
const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

type RenderedCard = {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}

async function renderCard(
  props: Partial<React.ComponentProps<typeof RechargeFormCard>> = {}
): Promise<RenderedCard> {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(
      <I18nextProvider i18n={i18n}>
        <RechargeFormCard
          topupInfo={{
            enable_online_topup: false,
            enable_stripe_topup: false,
            pay_methods: [],
            min_topup: 1,
            stripe_min_topup: 1,
            amount_options: [],
            discount: {},
            enable_redemption: true,
          }}
          presetAmounts={[]}
          selectedPreset={null}
          onSelectPreset={() => undefined}
          topupAmount={0}
          onTopupAmountChange={() => undefined}
          paymentAmount={0}
          calculating={false}
          onPaymentMethodSelect={() => undefined}
          paymentLoading={null}
          redemptionCode=''
          onRedemptionCodeChange={() => undefined}
          onRedeem={() => undefined}
          redeeming={false}
          {...props}
        />
      </I18nextProvider>
    )
  })

  return { container, root }
}

async function unmountCard(rendered: RenderedCard) {
  await act(async () => rendered.root.unmount())
  rendered.container.remove()
}

describe('recharge form card', () => {
  after(() => {
    domWindow.close()
  })

  test('shows a configured redemption-code purchase link when online topup is disabled', async () => {
    const purchaseUrl = 'https://pay.example.com/redemption-codes'
    const rendered = await renderCard({ topupLink: purchaseUrl })

    const link = [...rendered.container.querySelectorAll('a')].find((item) =>
      item.textContent?.includes('Buy point redemption codes')
    )
    assert.ok(link)
    assert.equal(link.getAttribute('href'), purchaseUrl)
    assert.equal(link.getAttribute('target'), '_blank')
    assert.equal(link.getAttribute('rel'), 'noopener noreferrer')

    await unmountCard(rendered)
  })
})
