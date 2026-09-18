<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-1 flex-wrap items-center justify-between gap-3">
          <div>
            <h1 class="text-lg font-semibold text-gray-900 dark:text-white">防降智</h1>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
              管理正常调用 IP、292 专用 IP 和 Codex turn state
            </p>
          </div>
          <button class="btn btn-secondary" :disabled="loading" @click="loadOverview">
            <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
          </button>
        </div>
      </template>

      <template #table>
        <div class="flex min-h-0 flex-1 flex-col gap-4 overflow-auto">
          <div class="grid grid-cols-2 gap-3 lg:grid-cols-5">
            <div class="rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-700 dark:bg-dark-800">
              <div class="text-xs text-gray-500 dark:text-dark-400">OpenAI OAuth</div>
              <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ accounts.length }}</div>
            </div>
            <div class="rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-700 dark:bg-dark-800">
              <div class="text-xs text-gray-500 dark:text-dark-400">292 自动已开</div>
              <div class="mt-1 text-xl font-semibold text-emerald-600 dark:text-emerald-400">{{ enabledCount }}</div>
            </div>
            <div class="rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-700 dark:bg-dark-800">
              <div class="text-xs text-gray-500 dark:text-dark-400">降智账号</div>
              <div class="mt-1 text-xl font-semibold text-rose-600 dark:text-rose-400">{{ degradedCount }}</div>
            </div>
            <div class="rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-700 dark:bg-dark-800">
              <div class="text-xs text-gray-500 dark:text-dark-400">获取中</div>
              <div class="mt-1 text-xl font-semibold text-primary-600 dark:text-primary-400">{{ runningCount }}</div>
            </div>
            <div class="rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-700 dark:bg-dark-800">
              <div class="text-xs text-gray-500 dark:text-dark-400">有效 State</div>
              <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ validStateCount }}</div>
            </div>
          </div>

          <div v-if="loading && accounts.length === 0" class="flex flex-1 items-center justify-center py-20 text-gray-500">
            <Icon name="refresh" size="lg" class="mr-2 animate-spin" /> 加载中
          </div>

          <div v-else class="table-wrapper rounded-lg border border-gray-200 dark:border-dark-700">
            <table class="anti-degrade-table table-fixed divide-y divide-gray-200 text-sm dark:divide-dark-700">
              <thead class="bg-gray-50 dark:bg-dark-800">
                <tr class="text-left text-xs font-medium text-gray-500 dark:text-dark-400">
                  <th class="w-[15%] px-4 py-3">账号</th>
                  <th class="w-[19%] px-4 py-3">正常调用 IP</th>
                  <th class="w-[20%] px-4 py-3">292 专用 IP</th>
                  <th class="w-[15%] px-4 py-3">自动获取</th>
                  <th class="w-[18%] px-4 py-3">State 状态</th>
                  <th class="w-[13%] px-4 py-3 text-right">操作</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-800 dark:bg-dark-900">
                <tr v-for="account in accounts" :key="account.id">
                  <td class="px-4 py-3 align-top">
                    <div class="font-medium text-gray-900 dark:text-white">{{ account.name }}</div>
                    <div class="mt-1 flex items-center gap-2 text-xs">
                      <span class="text-gray-400">#{{ account.id }}</span>
                      <span :class="account.status === 'active' ? 'text-emerald-600' : 'text-gray-400'">
                        {{ account.status }}
                      </span>
                      <span
                        v-if="account.degraded"
                        class="rounded bg-rose-100 px-1.5 py-0.5 font-medium text-rose-700 dark:bg-rose-950 dark:text-rose-300"
                      >
                        降智
                      </span>
                    </div>
                  </td>
                  <td class="px-4 py-3 align-top text-xs leading-5 text-gray-700 dark:text-dark-300">
                    <template v-if="account.normal_proxy_id">
                      <div class="font-medium text-gray-800 dark:text-dark-200">{{ proxyNameLabel(account.normal_proxy_id) }}</div>
                      <div class="mt-0.5 text-gray-500 dark:text-dark-400">{{ proxyAddressLabel(account.normal_proxy_id) }}</div>
                    </template>
                    <span v-else>直连</span>
                  </td>
                  <td class="px-4 py-3 align-top text-xs leading-5 text-gray-700 dark:text-dark-300">
                    <template v-if="account.state_proxy_ids?.length">
                      <div v-for="proxyID in account.state_proxy_ids" :key="proxyID" class="mb-1.5 last:mb-0">
                        <div class="font-medium text-gray-800 dark:text-dark-200">{{ proxyNameLabel(proxyID) }}</div>
                        <div class="mt-0.5 text-gray-500 dark:text-dark-400">{{ proxyAddressLabel(proxyID) }}</div>
                      </div>
                    </template>
                    <span v-else>跟随正常调用 IP</span>
                  </td>
                  <td class="px-4 py-3 align-top">
                    <span
                      class="inline-flex rounded px-2 py-1 text-xs font-medium"
                      :class="account.auto_mint
                        ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300'
                        : 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-400'"
                    >
                      {{ account.auto_mint ? '已开启' : '未开启' }}
                    </span>
                    <span class="ml-2 text-xs text-gray-500">
                      并发 {{ account.concurrency }} · 提前 {{ account.refresh_before_minutes }} 分
                    </span>
                  </td>
                  <td class="px-4 py-3 align-top">
                    <div v-if="(account.models || []).length === 0" class="text-xs text-gray-400">暂无 state 记录</div>
                    <div v-for="model in account.models" :key="model.model" class="mb-3 last:mb-0">
                      <div class="flex flex-wrap items-center gap-2">
                        <span class="font-medium text-gray-800 dark:text-dark-200">{{ model.model }}</span>
                        <span v-if="model.degraded" class="text-xs text-rose-600">312</span>
                        <span v-else-if="model.current" class="text-xs text-emerald-600">292</span>
                        <span v-if="model.mint_run.running" class="inline-flex items-center text-xs text-primary-600">
                          <Icon name="refresh" size="xs" class="mr-1 animate-spin" />
                          第 {{ model.mint_run.attempts + 1 }} 次获取中
                        </span>
                      </div>
                      <div v-if="model.current" class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                        current {{ model.current.length }} 字符 · {{ expiresText(model.current.expires_at) }}
                        <span v-if="model.previous"> · previous 兜底 {{ model.previous.length }} 字符</span>
                      </div>
                      <div v-if="model.mint_run.last_error" class="mt-1 max-w-md truncate text-xs text-rose-500" :title="model.mint_run.last_error">
                        {{ model.mint_run.last_error }}
                      </div>
                    </div>
                  </td>
                  <td class="whitespace-nowrap px-4 py-3 align-top text-right">
                    <div class="flex justify-end gap-2">
                      <button class="btn btn-secondary !px-3 !py-1.5 text-xs" @click="openConfig(account)">
                        <Icon name="cog" size="sm" />
                      </button>
                      <button class="btn btn-primary whitespace-nowrap !px-3 !py-1.5 text-xs" @click="triggerDefaultMint(account)">
                        <Icon name="play" size="sm" class="mr-1" />
                        获取292
                      </button>
                    </div>
                  </td>
                </tr>
                <tr v-if="accounts.length === 0">
                  <td colspan="6" class="px-4 py-16 text-center text-gray-500">没有 OpenAI OAuth 账号</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>
    </TablePageLayout>

    <BaseDialog :show="showConfig" title="防降智配置" width="wide" @close="showConfig = false">
      <div v-if="editingAccount" class="space-y-5">
        <div>
          <div class="text-sm font-medium text-gray-900 dark:text-white">{{ editingAccount.name }}</div>
          <div class="mt-1 text-xs text-gray-500">账号 #{{ editingAccount.id }}</div>
        </div>

        <label class="flex items-center justify-between rounded-lg border border-gray-200 p-3 dark:border-dark-700">
          <span>
            <span class="block text-sm font-medium text-gray-900 dark:text-white">启用 292 自动获取</span>
            <span class="mt-0.5 block text-xs text-gray-500">开启后立即开始 292 接管；后续自动续期，失败会持续重试</span>
          </span>
          <input v-model="form.autoMint" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600" />
        </label>

        <div>
          <label class="mb-2 block text-sm font-medium text-gray-900 dark:text-white">292 并发数量</label>
          <input
            v-model.number="form.concurrency"
            type="number"
            min="1"
            max="20"
            class="input w-full"
          />
          <p class="mt-1 text-xs text-gray-500">范围 1-20。每轮按该并发数探测；全部失败后固定间隔 3 秒继续下一轮，直到成功或关闭自动获取。</p>
        </div>

        <div>
          <label class="mb-2 block text-sm font-medium text-gray-900 dark:text-white">
            剩余多少分钟开始获取下一个 292
          </label>
          <input
            v-model.number="form.refreshBeforeMinutes"
            type="number"
            min="1"
            max="60"
            class="input w-full"
          />
          <p class="mt-1 text-xs text-gray-500">范围 1-60 分钟，默认 10 分钟。新 state 获取并复测成功后才替换 current。</p>
        </div>

        <div>
          <label class="mb-2 block text-sm font-medium text-gray-900 dark:text-white">正常调用 IP</label>
          <select v-model.number="form.normalProxyID" class="input w-full">
            <option :value="0">直连（不绑定代理）</option>
            <option v-for="proxy in proxies" :key="proxy.id" :value="proxy.id">{{ proxyLabel(proxy.id) }}</option>
          </select>
          <p class="mt-1 text-xs text-gray-500">用于账号日常对话请求，写入账号本身的 proxy_id。</p>
        </div>

        <div>
          <label class="mb-2 block text-sm font-medium text-gray-900 dark:text-white">292 专用 IP</label>
          <div class="grid max-h-56 grid-cols-1 gap-2 overflow-y-auto rounded-lg border border-gray-200 p-3 md:grid-cols-2 dark:border-dark-700">
            <label v-for="proxy in proxies" :key="proxy.id" class="flex items-center gap-2 text-sm text-gray-700 dark:text-dark-300">
              <input v-model="form.stateProxyIDs" type="checkbox" :value="proxy.id" class="h-4 w-4 rounded border-gray-300 text-primary-600" />
              {{ proxyLabel(proxy.id) }}
            </label>
            <div v-if="proxies.length === 0" class="text-sm text-gray-500">暂无代理，请先到 IP 管理创建。</div>
          </div>
          <p class="mt-1 text-xs text-gray-500">
            只用于 292 获取。列表里的 rotating 代理会按顺序使用；单个 region-Rand/rotate 代理会连续重试。
          </p>
        </div>
      </div>

      <template #footer>
        <button class="btn btn-secondary" @click="showConfig = false">取消</button>
        <button class="btn btn-primary" :disabled="saving" @click="saveConfig">
          <Icon v-if="saving" name="refresh" size="sm" class="mr-1 animate-spin" />
          保存
        </button>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { codexStateAPI, type CodexStateAccount, type CodexStateProxy } from '@/api/admin/codexState'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

const appStore = useAppStore()
const loading = ref(false)
const saving = ref(false)
const accounts = ref<CodexStateAccount[]>([])
const proxies = ref<CodexStateProxy[]>([])
const defaultModel = ref('gpt-6-astra')
const showConfig = ref(false)
const editingAccount = ref<CodexStateAccount | null>(null)
const form = reactive({
  autoMint: false,
  concurrency: 1,
  refreshBeforeMinutes: 10,
  normalProxyID: 0,
  stateProxyIDs: [] as number[]
})
let refreshTimer: ReturnType<typeof setInterval> | undefined

const enabledCount = computed(() => accounts.value.filter((item) => item.auto_mint).length)
const degradedCount = computed(() => accounts.value.filter((item) => item.degraded).length)
const runningCount = computed(() => accounts.value.filter((item) => (item.models || []).some((model) => model.mint_run.running)).length)
const validStateCount = computed(() => accounts.value.reduce((count, account) => (
  count + (account.models || []).filter((model) => model.current).length
), 0))

function proxyLabel(id?: number) {
  if (!id) return '直连'
  const proxy = proxies.value.find((item) => item.id === id)
  return proxy ? `#${proxy.id} ${proxy.name} (${proxy.host}:${proxy.port})` : `#${id}`
}

function proxyNameLabel(id?: number) {
  if (!id) return ''
  const proxy = proxies.value.find((item) => item.id === id)
  return proxy ? `#${proxy.id} ${proxy.name}` : `#${id}`
}

function proxyAddressLabel(id?: number) {
  if (!id) return ''
  const proxy = proxies.value.find((item) => item.id === id)
  return proxy ? `${proxy.host}:${proxy.port}` : ''
}

function expiresText(value: string) {
  const remainingMs = new Date(value).getTime() - Date.now()
  if (remainingMs <= 0) return '已过期'
  const minutes = Math.ceil(remainingMs / 60000)
  if (minutes < 60) return `${minutes} 分钟后过期`
  return `${Math.floor(minutes / 60)} 小时 ${minutes % 60} 分钟后过期`
}

async function loadOverview() {
  loading.value = true
  try {
    const overview = await codexStateAPI.getOverview()
    accounts.value = overview.accounts
    proxies.value = overview.proxies
    defaultModel.value = overview.default_model || defaultModel.value
    if (editingAccount.value) {
      editingAccount.value = accounts.value.find((item) => item.id === editingAccount.value?.id) || null
    }
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, '加载防降智状态失败'))
  } finally {
    loading.value = false
  }
}

function openConfig(account: CodexStateAccount) {
  editingAccount.value = account
  form.autoMint = account.auto_mint
  form.concurrency = account.concurrency || 1
  form.refreshBeforeMinutes = account.refresh_before_minutes || 10
  form.normalProxyID = account.normal_proxy_id || 0
  form.stateProxyIDs = [...account.state_proxy_ids]
  showConfig.value = true
}

async function saveConfig() {
  if (!editingAccount.value) return
  saving.value = true
  try {
    await codexStateAPI.updateAccount(editingAccount.value.id, {
      auto_mint: form.autoMint,
      concurrency: form.concurrency,
      refresh_before_minutes: form.refreshBeforeMinutes,
      normal_proxy_id: form.normalProxyID,
      state_proxy_ids: form.stateProxyIDs
    })
    showConfig.value = false
    await loadOverview()
    appStore.showSuccess('防降智配置已保存')
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, '保存防降智配置失败'))
  } finally {
    saving.value = false
  }
}

async function triggerDefaultMint(account: CodexStateAccount) {
  try {
    await codexStateAPI.triggerMint(account.id, account.models?.[0]?.model || defaultModel.value)
    await loadOverview()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, '启动 292 获取失败'))
  }
}

onMounted(() => {
  loadOverview()
  refreshTimer = setInterval(loadOverview, 10000)
})

onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
})
</script>

<style scoped>
.anti-degrade-table {
  min-width: 100% !important;
}

.anti-degrade-table :deep(th),
.anti-degrade-table :deep(td) {
  overflow-wrap: anywhere;
}
</style>
