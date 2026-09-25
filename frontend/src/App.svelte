<script lang="ts">
  import { onMount } from 'svelte'
  import { Events } from '@wailsio/runtime'
  import AccountCard from './features/overview/AccountCard.svelte'
  import JoinForm from './features/overview/JoinForm.svelte'
  import Settings from './features/settings/Settings.svelte'
  import { Desktop, errorMessage, type ActiveUser, type ProviderState, type State } from './lib/api'
  import { clock, providerName } from './lib/format'
  import { setLocale, t } from './lib/i18n.svelte'

  let app = $state<State | null>(null)
  let view = $state<'overview' | 'settings'>('overview')
  let loadError = $state('')
  let now = $state(new Date())

  function providerIssue(p: ProviderState): string {
    if (p.status === 'error') return p.error ?? t('error')
    if (p.status === 'not_installed') return t('notInstalled')
    if (p.status === 'not_pooled') return t('notPooled')
    return ''
  }

  const issues = $derived(
    (app?.providers ?? []).filter((p) => p.status === 'error' || p.status === 'not_pooled'),
  )
  const accounts = $derived(app?.overview?.accounts ?? [])
  // Before joining, only this device's own sign-in is known.
  const youHere: ActiveUser = { person_id: '', name: '', is_you: true, devices: [] }

  $effect(() => setLocale(app?.language ?? 'en'))

  onMount(() => {
    Desktop.State()
      .then((s) => (app = s))
      .catch((err) => (loadError = errorMessage(err)))
    const off = Events.On('state', (ev: { data: State }) => (app = ev.data))
    const timer = setInterval(() => (now = new Date()), 30_000)
    return () => {
      off()
      clearInterval(timer)
    }
  })
</script>

<main>
  <header>
    <h1>{view === 'settings' ? t('settings') : 'ShareCodex'}</h1>
    <div class="tools">
      {#if view === 'overview'}
        <button class="icon" title={t('refresh')} aria-label={t('refresh')} onclick={() => Desktop.Refresh()}>
          <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12.76 5.75A5.5 5.5 0 1 1 8 3M6.5 1.2 8.3 3 6.5 4.8" /></svg>
        </button>
        <button class="icon" title={t('settings')} aria-label={t('settings')} onclick={() => (view = 'settings')}>
          <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><circle cx="8" cy="8" r="2.2" /><path d="M8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2M3.4 3.4l1.4 1.4M11.2 11.2l1.4 1.4M3.4 12.6l1.4-1.4M11.2 4.8l1.4-1.4" stroke-linecap="round" /></svg>
        </button>
      {:else}
        <button class="icon" onclick={() => (view = 'overview')}>{t('done')}</button>
      {/if}
    </div>
  </header>

  <div class="content">
    {#if loadError}
      <p class="error">{loadError}</p>
    {:else if !app}
      <p class="muted">{t('loading')}</p>
    {:else if view === 'settings'}
      <Settings {app} />
    {:else}
      {#if app.update}
        <p class="banner update">
          <span>{t('newVersion', { version: app.update.version })}</span>
          <button onclick={() => Desktop.OpenReleasePage()}>{t('download')}</button>
        </p>
      {/if}
      {#if app.revoked}
        <p class="banner error">{t('revoked')}</p>
      {/if}
      {#if !app.paired || app.revoked}
        <JoinForm />
      {/if}

      {#each issues as p (p.provider)}
        <p class="banner"><strong>{providerName(p.provider)}</strong>：{providerIssue(p)}</p>
      {/each}

      {#if app.paired && accounts.length > 0}
        {#each accounts as a (a.id)}
          <AccountCard provider={a.provider} label={a.label} planType={a.plan_type} buckets={a.buckets ?? []} activeUsers={a.active_users ?? []} {now} />
        {/each}
      {:else}
        {#each app.local ?? [] as a (a.provider + a.hint)}
          <AccountCard provider={a.provider} label={a.hint} planType={a.plan_type} buckets={a.buckets ?? []} activeUsers={a.current ? [youHere] : []} {now} local />
        {/each}
        {#if (app.local ?? []).length === 0 && app.paired}
          <p class="muted">{t('noQuota')}</p>
        {/if}
      {/if}
    {/if}
  </div>

  {#if app && view === 'overview' && app.paired}
    <footer class="muted">
      <div class="status">
        {#if app.sync_error && !app.revoked}
          <span class="error">{t('syncFailed', { error: app.sync_error })}</span>
        {:else if app.last_sync_at}
          <span>{t('synced', { time: clock(app.last_sync_at) })}</span>
        {:else}
          <span>{t('syncing')}</span>
        {/if}
        {#if app.pending_uploads > 0}<span class="num">{t('pending', { count: app.pending_uploads })}</span>{/if}
      </div>
      <span class="num">v{app.version}</span>
    </footer>
  {/if}
</main>

<style>
  main { height: 100%; display: flex; flex-direction: column; }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    /* Fixed so switching between the overview's icons and settings' Done
       button does not move the title. */
    flex: none;
    height: 44px;
    padding: 0 14px;
    border-bottom: 1px solid var(--line);
    --wails-draggable: drag;
  }
  h1 { margin: 0; font-size: 14px; font-weight: 600; }
  .tools { display: flex; gap: 2px; --wails-draggable: no-drag; }
  .content {
    flex: 1;
    overflow-y: auto;
    padding: 14px;
    display: grid;
    align-content: start;
    gap: 10px;
  }
  .banner {
    margin: 0;
    padding: 8px 10px;
    border-radius: 8px;
    background: var(--warn-soft);
    color: var(--text);
    overflow-wrap: anywhere;
  }
  .banner.update { display: flex; justify-content: space-between; align-items: center; background: var(--accent-soft); }
  .banner.error { background: none; border: 1px solid var(--danger); color: var(--danger); }
  footer {
    display: flex;
    justify-content: space-between;
    gap: 8px;
    padding: 8px 14px;
    border-top: 1px solid var(--line);
    font-size: 11.5px;
  }
  footer .status { display: flex; gap: 8px; min-width: 0; }
  footer .error { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  p { margin: 0; }
</style>
