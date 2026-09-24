<script lang="ts">
  import { Desktop, errorMessage, type State } from '../../lib/api'
  import { locales, t } from '../../lib/i18n.svelte'

  let { app }: { app: State } = $props()

  let busy = $state(false)
  let error = $state('')
  let confirmingLeave = $state(false)

  async function run(action: () => Promise<void>) {
    busy = true
    error = ''
    try {
      await action()
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = false
    }
  }
</script>

<div class="settings">
  {#if error}<p class="error">{error}</p>{/if}

  <section>
    <h2>{t('server')}</h2>
    {#if app.paired}
      <dl>
        <dt>{t('member')}</dt><dd>{app.person_name}</dd>
        <dt>{t('device')}</dt><dd>{app.device_name}</dd>
        <dt>{t('address')}</dt><dd class="url">{app.server_url}</dd>
      </dl>
      {#if confirmingLeave}
        <div class="confirm">
          <p><strong>{t('leaveConfirm')}</strong><br /><span class="muted">{t('leaveBody')}</span></p>
          <div class="actions">
            <button onclick={() => (confirmingLeave = false)} disabled={busy}>{t('cancel')}</button>
            <button class="danger" onclick={() => run(async () => { await Desktop.Leave(); confirmingLeave = false })} disabled={busy}>{t('leave')}</button>
          </div>
        </div>
      {:else}
        <button class="danger" onclick={() => (confirmingLeave = true)}>{t('leaveServer')}</button>
      {/if}
    {:else}
      <p class="muted">{t('notJoined')}</p>
    {/if}
  </section>

  <section>
    <h2>{t('claudeQuota')}</h2>
    <p class="muted">{t('claudeQuotaBody')}</p>
    <div class="row">
      <span>{t('statusLineCapture')}</span>
      {#if app.status_line_installed}
        <button onclick={() => run(() => Desktop.RestoreStatusLine())} disabled={busy}>{t('turnOff')}</button>
      {:else}
        <button class="primary" onclick={() => run(() => Desktop.InstallStatusLine())} disabled={busy}>{t('turnOn')}</button>
      {/if}
    </div>
  </section>

  <section>
    <h2>{t('general')}</h2>
    <label class="row">
      <span>{t('language')}</span>
      <select value={app.language || 'en'} disabled={busy}
        onchange={(e) => run(() => Desktop.SetLanguage(e.currentTarget.value))}>
        {#each locales as l (l.id)}<option value={l.id}>{l.label}</option>{/each}
      </select>
    </label>
    <label class="row">
      <span>{t('launchAtLogin')}</span>
      <input type="checkbox" checked={app.launch_at_login} disabled={busy}
        onchange={(e) => run(() => Desktop.SetLaunchAtLogin(e.currentTarget.checked))} />
    </label>
    <div class="row">
      <span class="muted">{t('version', { version: app.version })}</span>
      <button onclick={() => Desktop.Quit()}>{t('quit')}</button>
    </div>
  </section>
</div>

<style>
  .settings { display: grid; gap: 10px; }
  section {
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 12px 14px;
    display: grid;
    gap: 8px;
    justify-items: start;
  }
  h2 { margin: 0; font-size: 13px; font-weight: 600; }
  p { margin: 0; }
  dl { margin: 0; display: grid; grid-template-columns: auto 1fr; gap: 3px 12px; width: 100%; }
  dt { color: var(--muted); }
  dd { margin: 0; min-width: 0; }
  .url { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .row { display: flex; justify-content: space-between; align-items: center; width: 100%; }
  .confirm { display: grid; gap: 8px; width: 100%; }
  .actions { display: flex; gap: 6px; justify-content: flex-end; }
</style>
