<script lang="ts">
  import { onMount } from 'svelte'
  import { Events } from '@wailsio/runtime'
  import AccountCard from '../overview/AccountCard.svelte'
  import { Desktop, errorMessage, type State } from '../../lib/api'
  import { setLocale, t } from '../../lib/i18n.svelte'

  // One pinned account in an always-on-top window of its own. It follows
  // the same state as the popup and sizes its window to its card whenever
  // the card's height changes; a window too short for the card, on a small
  // screen or made shorter by hand, scrolls.
  let { accountId }: { accountId: string } = $props()

  let app = $state<State | null>(null)
  let loadError = $state('')
  let now = $state(new Date())
  let box: HTMLElement

  const account = $derived((app?.overview?.accounts ?? []).find((a) => a.id === accountId))

  $effect(() => setLocale(app?.language ?? 'en'))

  function close() {
    Desktop.UnpinAccount(accountId).catch((err) => (loadError = errorMessage(err)))
  }

  onMount(() => {
    Desktop.State()
      .then((s) => (app = s))
      .catch((err) => (loadError = errorMessage(err)))
    const off = Events.On('state', (ev: { data: State }) => (app = ev.data))
    const timer = setInterval(() => (now = new Date()), 30_000)
    const resize = new ResizeObserver(() => Desktop.SetWidgetHeight(accountId, Math.ceil(box.getBoundingClientRect().height)))
    resize.observe(box)
    return () => {
      off()
      clearInterval(timer)
      resize.disconnect()
    }
  })
</script>

<main>
<div bind:this={box}>
  {#if account}
    <AccountCard id={account.id} provider={account.provider} label={account.label} planType={account.plan_type}
      buckets={account.buckets ?? []} activeUsers={account.active_users ?? []} {now} fineChart={app?.fine_chart}
      onClose={close} />
  {:else}
    <section class="gone">
      <span class={loadError ? 'error' : 'muted'}>{loadError || (app ? t('accountGone') : t('loading'))}</span>
      <button class="corner" title={t('close')} aria-label={t('close')} onclick={close}>
        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M2.5 2.5l7 7M9.5 2.5l-7 7" /></svg>
      </button>
    </section>
  {/if}
</div>
</main>

<style>
  main { height: 100%; overflow-y: auto; overscroll-behavior: contain; }
  .gone {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 14px;
    --wails-draggable: drag;
  }
  .corner { display: flex; padding: 3px; border: none; background: none; color: var(--muted); --wails-draggable: no-drag; }
  .corner:hover { color: var(--text); }
  .error { color: var(--danger); }
</style>
