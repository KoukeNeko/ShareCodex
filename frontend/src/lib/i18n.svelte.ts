// UI strings. English is the default; the chosen language is saved in the
// app's settings and applied from State.language.

export type Locale = 'en' | 'zh-TW'

export const locales: { id: Locale; label: string }[] = [
  { id: 'en', label: 'English' },
  { id: 'zh-TW', label: '繁體中文' },
]

const en = {
  refresh: 'Refresh',
  settings: 'Settings',
  done: 'Done',
  loading: 'Loading…',
  newVersion: 'Version {version} available',
  download: 'Download',
  revoked: 'This device was revoked. Ask an admin for a new join link.',
  noQuota: 'No quota data yet',
  syncFailed: 'Sync failed: {error}',
  synced: 'Synced {time}',
  syncing: 'Syncing',
  pending: '{count} to upload',
  notInstalled: 'Not installed',
  notPooled: 'Not signed in with a subscription',
  error: 'Error',
  account: 'Account',
  reset: 'Reset',
  you: 'You',
  inUse: 'In use',
  overAllotment: 'Over allotment',
  estimated: 'Est. {percent}',
  allotted: 'Allotted {percent}',
  unattributed: 'Unattributed',
  models: 'Models',
  tokens: '{count} tokens',
  joinServer: 'Join a server',
  pasteLink: 'Paste a join link',
  join: 'Join',
  server: 'Server',
  member: 'Member',
  device: 'Device',
  address: 'Address',
  leaveConfirm: 'Leave the server?',
  leaveBody: 'Usage already uploaded stays on the server.',
  cancel: 'Cancel',
  leave: 'Leave',
  leaveServer: 'Leave server',
  addDevice: 'Add device',
  inviteNote: 'Copied. Single use, valid until {time}.',
  notJoined: 'Not joined',
  claudeQuota: 'Claude Code quota',
  claudeQuotaBody:
    "Claude's 5-hour and weekly quota is only available through the statusLine. Your existing statusLine keeps working.",
  statusLineCapture: 'statusLine capture',
  turnOn: 'Turn on',
  turnOff: 'Turn off',
  general: 'General',
  launchAtLogin: 'Open at login',
  language: 'Language',
  version: 'Version {version}',
  quit: 'Quit ShareCodex',
  fiveHour: '5 hours',
  weekly: 'Weekly',
  days: '{count} days',
  hours: '{count} hours',
  minutes: '{count} min',
  resetsInDays: 'Resets in {days}d {hours}h',
  resetsInHours: 'Resets in {hours}h {minutes}m',
  resetsInMinutes: 'Resets in {minutes}m',
}

export type MessageKey = keyof typeof en

const zhTW: Record<MessageKey, string> = {
  refresh: '重新整理',
  settings: '設定',
  done: '完成',
  loading: '載入中…',
  newVersion: '新版本 {version}',
  download: '下載',
  revoked: '裝置已撤銷。請向管理員取得新的加入連結。',
  noQuota: '尚無額度資料',
  syncFailed: '同步失敗：{error}',
  synced: '已同步 {time}',
  syncing: '同步中',
  pending: '待上傳 {count} 筆',
  notInstalled: '未安裝',
  notPooled: '未使用訂閱帳號登入',
  error: '錯誤',
  account: '帳號',
  reset: '已重置',
  you: '你',
  inUse: '使用中',
  overAllotment: '超出分配',
  estimated: '估計 {percent}',
  allotted: '分配 {percent}',
  unattributed: '未歸屬',
  models: '模型',
  tokens: '{count} tokens',
  joinServer: '加入伺服器',
  pasteLink: '貼上加入連結',
  join: '加入',
  server: '伺服器',
  member: '成員',
  device: '裝置',
  address: '位址',
  leaveConfirm: '離開伺服器？',
  leaveBody: '已上傳的用量會保留在伺服器。',
  cancel: '取消',
  leave: '離開',
  leaveServer: '離開伺服器',
  addDevice: '新增裝置',
  inviteNote: '已複製。只能使用一次，{time} 前有效。',
  notJoined: '未加入',
  claudeQuota: 'Claude Code 額度',
  claudeQuotaBody: 'Claude 的 5 小時與每週額度只能從 statusLine 取得。啟用後，原本的 statusLine 仍照常顯示。',
  statusLineCapture: 'statusLine 擷取',
  turnOn: '啟用',
  turnOff: '停用',
  general: '一般',
  launchAtLogin: '登入時啟動',
  language: '語言',
  version: '版本 {version}',
  quit: '結束 ShareCodex',
  fiveHour: '5 小時',
  weekly: '每週',
  days: '{count} 天',
  hours: '{count} 小時',
  minutes: '{count} 分鐘',
  resetsInDays: '{days} 天 {hours} 小時後重置',
  resetsInHours: '{hours} 小時 {minutes} 分後重置',
  resetsInMinutes: '{minutes} 分後重置',
}

const messages: Record<Locale, Record<MessageKey, string>> = { en, 'zh-TW': zhTW }

let current = $state<Locale>('en')

export function isLocale(value: string): value is Locale {
  return value in messages
}

export function locale(): Locale {
  return current
}

export function setLocale(value: string) {
  current = isLocale(value) ? value : 'en'
  document.documentElement.lang = current === 'zh-TW' ? 'zh-Hant-TW' : 'en'
}

/** Translate a key, filling {name} placeholders from vars. */
export function t(key: MessageKey, vars: Record<string, string | number> = {}): string {
  return messages[current][key].replace(/\{(\w+)\}/g, (_, name: string) => String(vars[name] ?? `{${name}}`))
}
