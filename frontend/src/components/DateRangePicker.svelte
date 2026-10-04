<script lang="ts">
  import { locale, t } from '../lib/i18n.svelte'

  // The server calibrates over at most this many days back.
  const MAX_DAYS_BACK = 90

  let { range, onapply }: { range: { from: string; to: string } | null; onapply: (from: string, to: string) => void } = $props()

  const dayStart = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate())
  const addDays = (d: Date, n: number) => new Date(d.getFullYear(), d.getMonth(), d.getDate() + n)
  const monthStart = (d: Date) => new Date(d.getFullYear(), d.getMonth(), 1)

  let open = $state(false)
  let root = $state<HTMLElement>()
  let view = $state(monthStart(new Date()))
  let start = $state<Date | null>(null)
  let end = $state<Date | null>(null)

  const today = $derived(dayStart(new Date()))
  const oldest = $derived(addDays(today, -MAX_DAYS_BACK))

  const shortDate = (d: Date) => d.toLocaleDateString(locale(), { month: 'numeric', day: 'numeric' })
  // The stored range ends at the start of the day after its last day.
  const label = $derived(range ? `${shortDate(new Date(range.from))} – ${shortDate(addDays(new Date(range.to), -1))}` : t('customRange'))

  const weekdays = $derived(
    Array.from({ length: 7 }, (_, i) => new Intl.DateTimeFormat(locale(), { weekday: 'short' }).format(new Date(2023, 0, 1 + i))),
  )
  const monthTitle = $derived(new Intl.DateTimeFormat(locale(), { year: 'numeric', month: 'long' }).format(view))
  // Sunday-first grid: leading blanks, then the month's days.
  const cells = $derived.by(() => {
    const count = new Date(view.getFullYear(), view.getMonth() + 1, 0).getDate()
    const blanks = view.getDay()
    return Array.from({ length: blanks + count }, (_, i) => (i < blanks ? null : new Date(view.getFullYear(), view.getMonth(), i - blanks + 1)))
  })
  const canPrev = $derived(view > monthStart(oldest))
  const canNext = $derived(view < monthStart(today))

  const same = (a: Date | null, b: Date) => a !== null && a.getTime() === b.getTime()
  const inRange = (d: Date) => start !== null && end !== null && d > start && d < end

  function toggle() {
    open = !open
    if (!open) return
    start = range ? dayStart(new Date(range.from)) : null
    end = range ? addDays(new Date(range.to), -1) : null
    view = monthStart(start ?? today)
  }
  function pickDay(d: Date) {
    if (start === null || end !== null || d < start) {
      start = d
      end = null
    } else {
      end = d
    }
  }
  function apply() {
    if (start === null) return
    onapply(start.toISOString(), addDays(end ?? start, 1).toISOString())
    open = false
  }
  const shiftMonth = (n: number) => (view = new Date(view.getFullYear(), view.getMonth() + n, 1))

  function onKeydown(e: KeyboardEvent) {
    if (open && e.key === 'Escape') open = false
  }
  function onPointerdown(e: PointerEvent) {
    if (open && root && !root.contains(e.target as Node)) open = false
  }
</script>

<svelte:window onkeydown={onKeydown} onpointerdown={onPointerdown} />

<div class="picker" bind:this={root}>
  <button class="trigger" class:active={range} aria-expanded={open} onclick={toggle}>{label}</button>
  {#if open}
    <div class="panel">
      <div class="nav">
        <button class="icon" aria-label="‹" disabled={!canPrev} onclick={() => shiftMonth(-1)}>‹</button>
        <span>{monthTitle}</span>
        <button class="icon" aria-label="›" disabled={!canNext} onclick={() => shiftMonth(1)}>›</button>
      </div>
      <div class="grid">
        {#each weekdays as w (w)}<span class="weekday muted">{w}</span>{/each}
        {#each cells as d, i (i)}
          {#if d}
            <button
              class="day"
              class:range={inRange(d)}
              class:edge={same(start, d) || same(end, d)}
              class:today={same(today, d)}
              disabled={d < oldest || d > today}
              aria-pressed={same(start, d) || same(end, d)}
              onclick={() => pickDay(d)}>{d.getDate()}</button
            >
          {:else}
            <span></span>
          {/if}
        {/each}
      </div>
      <div class="actions">
        <button onclick={() => (open = false)}>{t('cancel')}</button>
        <button class="primary" disabled={start === null} onclick={apply}>{t('apply')}</button>
      </div>
    </div>
  {/if}
</div>

<style>
  /* Lays the trigger in the parent's row and the panel on its own line. */
  .picker { display: contents; }
  .trigger {
    font-size: 11.5px;
    padding: 1px 8px;
    border-radius: 999px;
    color: var(--muted);
  }
  .trigger.active { color: var(--text); border-color: var(--accent); }
  .panel {
    flex: 1 0 100%;
    max-width: 300px;
    display: grid;
    gap: 6px;
    padding: 8px;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--radius);
  }
  .nav { display: flex; align-items: center; justify-content: space-between; font-weight: 600; }
  .grid { display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); gap: 2px; text-align: center; font-size: 11.5px; }
  .weekday { padding: 2px 0; }
  .day {
    padding: 4px 0;
    border-color: transparent;
    background: none;
    border-radius: 6px;
    font-variant-numeric: tabular-nums;
  }
  .day.range { background: var(--accent-soft); }
  .day.today { border-color: var(--muted); }
  .day.edge, .day.edge:hover:not(:disabled) { background: var(--accent); border-color: var(--accent); color: #fff; }
  .actions { display: flex; justify-content: flex-end; gap: 6px; }
  .actions button { font-size: 11.5px; padding: 2px 10px; }
</style>
