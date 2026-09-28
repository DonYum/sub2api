<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.keyDirectory.title')"
    :show-close-button="!busy"
    :close-on-escape="!busy"
    @close="close"
  >
    <div v-if="user" class="space-y-4">
      <p class="break-all font-medium text-gray-900 dark:text-white">{{ user.email }} · #{{ user.id }}</p>
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('admin.users.keyDirectory.description') }}</p>
      <div>
        <label for="directory-expiry" class="mb-1 block text-sm font-medium">{{ t('admin.users.keyDirectory.expiry') }}</label>
        <input id="directory-expiry" v-model="expiresAt" type="datetime-local" class="input w-full" :disabled="busy" />
        <p class="mt-1 text-xs text-gray-500">{{ t('admin.users.keyDirectory.expiryHint') }}</p>
      </div>
      <p v-if="user.status !== 'active'" class="text-sm text-amber-700 dark:text-amber-400">{{ t('admin.users.keyDirectory.disabledUser') }}</p>
      <div class="flex flex-wrap gap-2">
        <button type="button" data-test="rotate" class="btn btn-primary" :disabled="busy || user.status !== 'active'" @click="prepare('rotate')">
          {{ t('admin.users.keyDirectory.rotate') }}
        </button>
        <button type="button" data-test="revoke" class="btn btn-danger" :disabled="busy" @click="prepare('revoke')">
          {{ t('admin.users.keyDirectory.revoke') }}
        </button>
      </div>
      <div v-if="pending" role="alert" class="space-y-3 rounded-xl border border-amber-300 p-4 dark:border-amber-700">
        <p class="text-sm">{{ t(`admin.users.keyDirectory.${pending}Confirm`) }}</p>
        <div class="flex gap-2">
          <button type="button" data-test="confirm" class="btn btn-primary" :disabled="busy" @click="execute">
            {{ busy ? t('common.processing') : t('common.confirm') }}
          </button>
          <button type="button" class="btn btn-secondary" :disabled="busy" @click="pending = null">{{ t('common.cancel') }}</button>
        </div>
      </div>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <div v-if="credential" class="space-y-3 rounded-xl border border-primary-200 p-4 dark:border-primary-800">
        <p class="text-sm font-medium">{{ t('admin.users.keyDirectory.copyOnce') }}</p>
        <label for="directory-credential" class="sr-only">{{ t('admin.users.keyDirectory.title') }}</label>
        <textarea id="directory-credential" :value="credential" readonly rows="3" class="input w-full break-all font-mono text-sm" spellcheck="false" />
        <button type="button" data-test="copy" class="btn btn-secondary" @click="copyToClipboard(credential)">{{ t('common.copy') }}</button>
        <p class="text-xs text-gray-500">{{ t('admin.users.keyDirectory.usage') }}</p>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { useClipboard } from '@/composables/useClipboard'
import type { AdminUser } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; user: AdminUser | null }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()
const appStore = useAppStore()
const { copyToClipboard } = useClipboard()
const expiresAt = ref('')
const credential = ref('')
const busy = ref(false)
const error = ref('')
const pending = ref<'rotate' | 'revoke' | null>(null)
let version = 0

function reset() {
  version++
  credential.value = ''
  expiresAt.value = ''
  pending.value = null
  busy.value = false
  error.value = ''
}
watch(() => [props.show, props.user?.id], reset)
onBeforeUnmount(reset)

function close() {
  if (busy.value) return
  reset()
  emit('close')
}
function prepare(action: 'rotate' | 'revoke') {
  error.value = ''
  pending.value = action
}
async function execute() {
  if (!props.show || !props.user || !pending.value || busy.value) return
  const action = pending.value
  const userID = props.user.id
  let expiry: string | null = null
  if (action === 'rotate' && expiresAt.value) {
    const date = new Date(expiresAt.value)
    if (!Number.isFinite(date.getTime()) || date.getTime() <= Date.now()) {
      error.value = t('admin.users.keyDirectory.invalidExpiry')
      return
    }
    expiry = date.toISOString()
  }
  const requestVersion = ++version
  busy.value = true
  credential.value = ''
  error.value = ''
  try {
    if (action === 'rotate') {
      const result = await adminAPI.users.rotateKeyDirectoryCredential(userID, expiry)
      if (requestVersion !== version || !props.show || props.user?.id !== userID) return
      credential.value = result.credential
    } else {
      await adminAPI.users.revokeKeyDirectoryCredential(userID)
      if (requestVersion !== version || !props.show || props.user?.id !== userID) return
      appStore.showSuccess(t('admin.users.keyDirectory.revoked'))
    }
    pending.value = null
  } catch {
    if (requestVersion === version) error.value = t('admin.users.keyDirectory.failed')
  } finally {
    if (requestVersion === version) busy.value = false
  }
}
</script>
