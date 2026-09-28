import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import UserKeyDirectoryModal from '../UserKeyDirectoryModal.vue'
import type { AdminUser } from '@/types'

const { rotate, revoke, copy, success } = vi.hoisted(() => ({ rotate: vi.fn(), revoke: vi.fn(), copy: vi.fn(), success: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { users: { rotateKeyDirectoryCredential: rotate, revokeKeyDirectoryCredential: revoke } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: success }) }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: copy }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => { vi.clearAllMocks(); rotate.mockResolvedValue({ user_id: 7, credential: 'kdir_test_secret', expires_at: null }); revoke.mockResolvedValue(undefined) })
const user = (id = 7, status = 'active') => ({ id, status, email: `user${id}@example.com` }) as AdminUser
const open = () => mount(UserKeyDirectoryModal, {
  props: { show: true, user: user() },
  global: { stubs: { BaseDialog: { props: ['show'], emits: ['close'], template: '<div v-if="show"><slot /><button data-test="close" @click="$emit(\'close\')">close</button></div>' } } }
})
async function confirm(wrapper: ReturnType<typeof open>, action: string) {
  await wrapper.get(`[data-test="${action}"]`).trigger('click')
  await wrapper.get('[data-test="confirm"]').trigger('click')
  await flushPromises()
}

describe('user key directory credential', () => {
  it('requires confirmation, creates for selected user, copies once and clears on close', async () => {
    const wrapper = open()
    await wrapper.get('[data-test="rotate"]').trigger('click')
    expect(rotate).not.toHaveBeenCalled()
    await wrapper.get('[data-test="confirm"]').trigger('click'); await flushPromises()
    expect(rotate).toHaveBeenCalledWith(7, null)
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('kdir_test_secret')
    await wrapper.get('[data-test="copy"]').trigger('click'); expect(copy).toHaveBeenCalledWith('kdir_test_secret')
    await wrapper.get('[data-test="close"]').trigger('click'); expect(wrapper.emitted('close')).toHaveLength(1)
    expect(wrapper.find('textarea').exists()).toBe(false)
  })
  it('revokes only the selected user and removes the displayed credential', async () => {
    const wrapper = open(); await confirm(wrapper, 'rotate'); await confirm(wrapper, 'revoke')
    expect(revoke).toHaveBeenCalledWith(7); expect(wrapper.find('textarea').exists()).toBe(false)
    expect(success).toHaveBeenCalledWith('admin.users.keyDirectory.revoked')
  })
  it('validates local expiry and converts a future time to UTC', async () => {
    const wrapper = open(); await wrapper.get('input').setValue('2000-01-01T12:00'); await confirm(wrapper, 'rotate')
    expect(rotate).not.toHaveBeenCalled(); expect(wrapper.text()).toContain('admin.users.keyDirectory.invalidExpiry')
    await wrapper.get('input').setValue('2099-01-01T12:00'); await wrapper.get('[data-test="confirm"]').trigger('click'); await flushPromises()
    expect(rotate).toHaveBeenCalledWith(7, new Date('2099-01-01T12:00').toISOString())
  })
  it('does not reveal a late credential after switching users', async () => {
    let resolve!: (value: unknown) => void
    rotate.mockReturnValueOnce(new Promise(res => { resolve = res }))
    const wrapper = open(); await confirm(wrapper, 'rotate')
    expect((wrapper.get('[data-test="confirm"]').element as HTMLButtonElement).disabled).toBe(true)
    await wrapper.get('[data-test="close"]').trigger('click'); expect(wrapper.emitted('close')).toBeUndefined()
    await wrapper.setProps({ user: user(8) })
    resolve({ user_id: 7, credential: 'stale-secret', expires_at: null }); await flushPromises()
    expect(wrapper.find('textarea').exists()).toBe(false); expect(wrapper.text()).toContain('user8@example.com')
  })
  it('clears secrets when hidden and ignores a late result while closed', async () => {
    let resolve!: (value: unknown) => void
    rotate.mockReturnValueOnce(new Promise(res => { resolve = res }))
    const wrapper = open(); await confirm(wrapper, 'rotate'); await wrapper.setProps({ show: false })
    resolve({ user_id: 7, credential: 'closed-secret', expires_at: null }); await flushPromises()
    await wrapper.setProps({ show: true }); expect(wrapper.find('textarea').exists()).toBe(false)
  })
  it('shows a safe error and allows retry without displaying the error payload', async () => {
    rotate.mockRejectedValueOnce(new Error('sensitive-backend-detail'))
    const wrapper = open(); await confirm(wrapper, 'rotate')
    expect(wrapper.text()).toContain('admin.users.keyDirectory.failed'); expect(wrapper.text()).not.toContain('sensitive-backend-detail')
    expect((wrapper.get('[data-test="confirm"]').element as HTMLButtonElement).disabled).toBe(false)
  })
  it('allows revocation but not creation for a disabled user', async () => {
    const wrapper = open(); await wrapper.setProps({ user: user(7, 'disabled') })
    expect((wrapper.get('[data-test="rotate"]').element as HTMLButtonElement).disabled).toBe(true)
    await confirm(wrapper, 'revoke'); expect(revoke).toHaveBeenCalledWith(7)
  })
})
