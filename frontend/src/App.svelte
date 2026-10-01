<script lang="ts">
  import { onMount } from 'svelte'
  import { flip } from 'svelte/animate'
  import { Events } from '@wailsio/runtime'
  import AccountCard from './features/overview/AccountCard.svelte'
  import JoinForm from './features/overview/JoinForm.svelte'
  import UsageStats from './features/overview/UsageStats.svelte'
  import Settings from './features/settings/Settings.svelte'
  import { Desktop, errorMessage, type ActiveUser, type ProviderState, type State } from './lib/api'
  import { clock, providerName } from './lib/format'
  import { renderShareImage } from './lib/share'
  import { sortAccounts } from './lib/sort'
  import { setLocale, t } from './lib/i18n.svelte'

  let app = $state<State | null>(null)
  let view = $state<'overview' | 'settings'>('overview')
  let loadError = $state('')
  let now = $state(new Date())
  let main: HTMLElement
  let sharing = $state(false)
  let shared = $state(false)
  let shareError = $state('')

  async function share() {
    sharing = true
    shareError = ''
    try {
      const png = await renderShareImage(main, (n) => t('shareAccount', { n }))
      await Desktop.CopyImage(png)
      shared = true
      setTimeout(() => (shared = false), 1500)
    } catch (err) {
      shareError = errorMessage(err)
    } finally {
      sharing = false
    }
  }

  // A short notice over the bottom of the popup.
  let toast = $state('')
  let toastTimer: ReturnType<typeof setTimeout> | undefined
  function showToast(text: string) {
    toast = text
    clearTimeout(toastTimer)
    toastTimer = setTimeout(() => (toast = ''), 2500)
  }

  async function openDashboard() {
    if (!app?.overview?.dashboard_published) {
      showToast(t('dashboardNotPublished'))
      return
    }
    try {
      await Desktop.OpenDashboard()
    } catch (err) {
      showToast(t('openDashboardFailed', { error: errorMessage(err) }))
    }
  }

  function providerIssue(p: ProviderState): string {
    if (p.status === 'error') return p.error ?? t('error')
    if (p.status === 'not_installed') return t('notInstalled')
    if (p.status === 'not_pooled') return t('notPooled')
    return ''
  }

  const issues = $derived(
    (app?.providers ?? []).filter((p) => p.status === 'error' || p.status === 'not_pooled'),
  )
  const accounts = $derived(
    sortAccounts(app?.overview?.accounts ?? [], app?.account_sort ?? '', app?.account_order ?? [], now),
  )

  // While a card is dragged, the list follows dragOrder; dropping it saves
  // the order, which also switches the popup to it.
  let content: HTMLElement
  let dragging = $state<string | null>(null)
  let dragOrder = $state<string[] | null>(null)
  let orderError = $state('')
  const shown = $derived(
    dragOrder ? dragOrder.map((id) => accounts.find((a) => a.id === id)!).filter(Boolean) : accounts,
  )

  let pinError = $state('')
  async function togglePin(id: string) {
    pinError = ''
    try {
      await ((app?.pinned ?? []).includes(id) ? Desktop.UnpinAccount(id) : Desktop.PinAccount(id))
    } catch (err) {
      pinError = errorMessage(err)
    }
  }

  function startDrag(e: PointerEvent, id: string) {
    e.preventDefault()
    const before = accounts.map((a) => a.id)
    dragOrder = before
    dragging = id
    const move = (ev: PointerEvent) => {
      // Near an edge, the list scrolls so a card can go past the view.
      const box = content.getBoundingClientRect()
      if (ev.clientY < box.top + 24) content.scrollTop -= 8
      else if (ev.clientY > box.bottom - 24) content.scrollTop += 8
      const next = [...content.querySelectorAll<HTMLElement>('[data-account]')].find((el) => {
        if (el.dataset.account === id) return false
        const r = el.getBoundingClientRect()
        return ev.clientY < r.top + r.height / 2
      })
      const order = dragOrder!.filter((x) => x !== id)
      order.splice(next ? order.indexOf(next.dataset.account!) : order.length, 0, id)
      if (order.join() !== dragOrder!.join()) dragOrder = order
    }
    const end = async () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', end)
      window.removeEventListener('pointercancel', end)
      const order = dragOrder!
      dragging = null
      if (order.join() !== before.join()) {
        orderError = ''
        try {
          await Desktop.SetAccountOrder(order)
          // Shown at once, before the agent's state event arrives.
          if (app) app = { ...app, account_sort: 'custom', account_order: order }
        } catch (err) {
          orderError = errorMessage(err)
        }
      }
      dragOrder = null
    }
    // On the window, not the grip: reordering moves the card's element,
    // which releases any pointer capture the grip held.
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', end)
    window.addEventListener('pointercancel', end)
  }
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

<main bind:this={main}>
  <header>
    <h1>{view === 'settings' ? t('settings') : 'ShareCodex'}</h1>
    <div class="tools">
      {#if view === 'overview'}
        {#if app?.paired}
          <button class="icon" title={t('openDashboard')} aria-label={t('openDashboard')} onclick={openDashboard}>
            <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="6.2" /><path d="M1.8 8h12.4M8 1.8c1.7 1.8 2.5 3.9 2.5 6.2s-.8 4.4-2.5 6.2M8 1.8C6.3 3.6 5.5 5.7 5.5 8s.8 4.4 2.5 6.2" /></svg>
          </button>
        {/if}
        <button class="icon" title={t('copyImage')} aria-label={t('copyImage')} disabled={sharing || !app} onclick={share}>
          {#if shared}
            <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 8.5 6.5 12 13 4.5" /></svg>
          {:else}
            <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 10V1.8M5.2 4.4 8 1.6l2.8 2.8M5 7H3.5v7h9V7H11" /></svg>
          {/if}
        </button>
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

  <div class="content" bind:this={content}>
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
      {#if shareError}
        <p class="banner error">{t('copyImageFailed', { error: shareError })}</p>
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

      {#if app.paired && app.overview?.you}
        <UsageStats usage={app.overview.you} />
      {/if}

      {#if orderError}
        <p class="banner error">{t('saveOrderFailed', { error: orderError })}</p>
      {/if}
      {#if pinError}
        <p class="banner error">{t('pinFailed', { error: pinError })}</p>
      {/if}
      {#if app.paired && accounts.length > 0}
        {#each shown as a (a.id)}
          <div class="account" class:dragging={dragging === a.id} data-account={a.id} animate:flip={{ duration: 150 }}>
            <AccountCard id={a.id} provider={a.provider} label={a.label} planType={a.plan_type} buckets={a.buckets ?? []} activeUsers={a.active_users ?? []} {now} fineChart={app.fine_chart}
              onGrip={accounts.length > 1 ? (e) => startDrag(e, a.id) : undefined}
              pinned={(app.pinned ?? []).includes(a.id)}
              onPin={() => togglePin(a.id)} />
          </div>
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

  {#if toast}<div class="toast" role="status">{toast}</div>{/if}

  {#if app && view === 'overview' && app.paired}
    <footer class="muted">
      <div class="status">
        {#if app.sync_error && !app.revoked}
          <span class="error">{t('syncFailed', { error: app.sync_error })}</span>
        {:else if app.overview_error}
          <span class="error">{t('overviewFailed', { error: app.overview_error })}</span>
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
  main { height: 100%; display: flex; flex-direction: column; position: relative; }
  .toast {
    position: absolute;
    left: 50%;
    bottom: 44px;
    transform: translateX(-50%);
    max-width: calc(100% - 28px);
    padding: 7px 12px;
    border-radius: 8px;
    background: var(--text);
    color: var(--bg);
    font-size: 12px;
    box-shadow: 0 4px 16px rgb(0 0 0 / .2);
    pointer-events: none;
    z-index: 2;
  }
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
    grid-template-columns: minmax(0, 1fr);
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
  .account { display: grid; grid-template-columns: minmax(0, 1fr); }
  .account.dragging { opacity: .85; }
  .account.dragging :global(.card) { border-color: var(--accent); box-shadow: 0 4px 16px rgb(0 0 0 / .18); }
</style>
