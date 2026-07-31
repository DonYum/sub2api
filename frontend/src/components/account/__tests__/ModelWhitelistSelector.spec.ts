import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { copyToClipboard, showError, syncUpstreamModels } = vi.hoisted(() => ({
  copyToClipboard: vi.fn().mockResolvedValue(true),
  showError: vi.fn(),
  syncUpstreamModels: vi.fn()
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (key === 'common.copy') return '复制'
        return params?.message ? `${key}: ${params.message}` : key
      }
    })
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/api/admin/accounts', () => ({
  accountsAPI: {
    syncUpstreamModels
  }
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard
  })
}))

import ModelWhitelistSelector from '../ModelWhitelistSelector.vue'

function mountSelector() {
  return mount(ModelWhitelistSelector, {
    props: {
      modelValue: [],
      platform: 'openai'
    },
    global: {
      stubs: {
        ModelIcon: true
      }
    }
  })
}

function findModelRow(wrapper: ReturnType<typeof mountSelector>, modelId: string) {
  const row = wrapper
    .findAll('[data-testid="model-option"]')
    .find(candidate => candidate.text().includes(modelId))

  if (!row) {
    throw new Error(`Model row not found: ${modelId}`)
  }

  return row
}

describe('ModelWhitelistSelector', () => {
  beforeEach(() => {
    copyToClipboard.mockClear()
    showError.mockClear()
    syncUpstreamModels.mockReset()
  })

  it('copies a model ID without selecting the model', async () => {
    const wrapper = mountSelector()
    await wrapper.get('div.cursor-pointer').trigger('click')

    const row = findModelRow(wrapper, 'gpt-5.6-sol')

    const copyButton = row.get('[data-testid="copy-model-id"]')
    expect(copyButton.attributes('aria-label')).toBe('复制 gpt-5.6-sol')

    await copyButton.trigger('click')
    await flushPromises()

    expect(copyToClipboard).toHaveBeenCalledWith('gpt-5.6-sol')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('keeps the existing model selection behavior', async () => {
    const wrapper = mountSelector()
    await wrapper.get('div.cursor-pointer').trigger('click')

    const row = findModelRow(wrapper, 'gpt-5.6-sol')
    await row.get('[data-testid="select-model"]').trigger('click')

    expect(wrapper.emitted('update:modelValue')).toEqual([[['gpt-5.6-sol']]])
    expect(copyToClipboard).not.toHaveBeenCalled()
  })

  it('shows the API error message when upstream model sync fails', async () => {
    syncUpstreamModels.mockRejectedValue({ message: 'Upstream returned HTTP 401' })
    const wrapper = mount(ModelWhitelistSelector, {
      props: {
        modelValue: [],
        platform: 'openai',
        accountId: 12
      },
      global: {
        stubs: {
          ModelIcon: true
        }
      }
    })

    const syncButton = wrapper.findAll('button').find(button => button.text().includes('syncUpstreamModels'))
    expect(syncButton).toBeDefined()
    await syncButton!.trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith(
      'admin.accounts.syncUpstreamModelsError: Upstream returned HTTP 401'
    )
  })
})
