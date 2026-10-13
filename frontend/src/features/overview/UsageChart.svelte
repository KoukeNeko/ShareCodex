<script lang="ts" module>
  // One choice for every card's chart.
  let cumulative = $state(true)
</script>

<script lang="ts">
  import type { MemberShare, Timeline } from '../../lib/api'
  import { modelName, tokens } from '../../lib/format'
  import { locale, t } from '../../lib/i18n.svelte'

  // Tokens over about the last window length, a line per model, for
  // everyone or one member: running totals, which start again from zero
  // where a window reset, or the amount in each point's span. Points group
  // the server's bins into about 60 (5 minutes on a 5-hour chart) unless
  // fine asks for every bin. Models keep the colors and order of the card's
  // Models list, its legend; third-party models, which never count against
  // the quota, are drawn dashed after them in their own list's order.
  // Beneath it, a short bar chart stacks every model's tokens in each hour,
  // or each day on a chart of days.
  let { timeline, models, thirdPartyModels = [], color, members, now, fine = false }: {
    timeline: Timeline
    models: { model: string; gateway: string }[]
    thirdPartyModels?: { model: string; gateway: string }[]
    color: (model: string, thirdParty: boolean, gateway: string) => string
    members: MemberShare[]
    now: Date
    fine?: boolean
  } = $props()

  let person = $state('')
  let width = $state(0)
  let hover = $state<number | null>(null)

  const height = 96
  const start = $derived(new Date(timeline.start).getTime())
  const binMs = $derived(timeline.bin_minutes * 60_000)
  // A chart of more than a day is labeled in days, and its bars are days.
  const days = $derived(timeline.bins * timeline.bin_minutes > 1440)
  // Bins after now have not happened yet, so their lines stop at now.
  const lastBin = $derived(Math.min(timeline.bins - 1, Math.max(0, Math.floor((now.getTime() - start) / binMs))))

  // Bins per point, and each point's bins [from, to).
  const group = $derived(fine ? 1 : Math.max(1, Math.ceil(timeline.bins / 60)))
  const lastPoint = $derived(Math.floor(lastBin / group))
  const binsOf = (point: number) => [point * group, Math.min((point + 1) * group, lastBin + 1)]

  // The bins in which a window starts again.
  const resetBins = $derived(new Set((timeline.resets ?? []).map((r) => Math.floor((new Date(r).getTime() - start) / binMs))))

  const seriesKey = (model: string, thirdParty: boolean, gateway = '') => `${thirdParty ? 'third' : 'quota'}:${gateway}:${model}`

  const series = $derived.by(() => {
    const byKey = new Map<string, { model: string; thirdParty: boolean; gateway: string; perBin: number[] }>()
    for (const p of timeline.points ?? []) {
      if (person && p.person_id !== person) continue
      if (p.bin < 0 || p.bin > lastBin) continue
      const key = seriesKey(p.model, !!p.third_party, p.gateway ?? '')
      let s = byKey.get(key)
      if (!s) byKey.set(key, (s = { model: p.model, thirdParty: !!p.third_party, gateway: p.gateway ?? '', perBin: new Array(lastBin + 1).fill(0) }))
      s.perBin[p.bin] += p.tokens
    }
    // The chart reaches back before the current window, so it can hold
    // models the window's lists lack: the lists' models come first, in their
    // order, and those used only earlier follow.
    const listed = [
      ...models.map((m) => seriesKey(m.model, false, m.gateway)),
      ...thirdPartyModels.map((m) => seriesKey(m.model, true, m.gateway)),
    ]
    const rank = (key: string) => (listed.includes(key) ? listed.indexOf(key) : listed.length)
    return [...byKey.entries()]
      .sort(([a], [b]) => rank(a) - rank(b))
      .map(([key, { model, thirdParty, gateway, perBin }]) => {
        const m = { model, thirdParty, gateway, key, perBin }
        const points = Array.from({ length: lastPoint + 1 }, (_, i) => binsOf(i))
        if (!cumulative) {
          // The tokens used within each point's span.
          return { ...m, values: points.map(([from, to]) => perBin.slice(from, to).reduce((a, v) => a + v, 0)) }
        }
        // The running total at the end of each point's span.
        let total = 0
        const totals = perBin.map((v, bin) => {
          if (resetBins.has(bin)) total = 0
          return (total += v)
        })
        return { ...m, values: points.map(([, to]) => totals[to - 1]) }
      })
  })

  // A clean top for an axis: 1, 2 or 5 times a power of ten.
  function cleanTop(max: number): number {
    if (max === 0) return 1
    const step = 10 ** Math.floor(Math.log10(max))
    return [1, 2, 5, 10].map((k) => k * step).find((v) => v >= max)!
  }
  const top = $derived(cleanTop(Math.max(0, ...series.flatMap((s) => s.values))))

  // A span's amount sits in its middle; a running total at its end.
  const x = (point: number) => {
    const [from, to] = binsOf(point)
    return ((cumulative ? to : (from + to) / 2) / timeline.bins) * width
  }
  // Where an earlier window reset, on the same time axis as the bins.
  const resets = $derived(
    (timeline.resets ?? []).map((r) => {
      const ms = new Date(r).getTime()
      return { x: ((ms - start) / (timeline.bins * binMs)) * width, text: resetLabel(ms) }
    }),
  )
  const y = (v: number) => height - (v / top) * (height - 4) - 1

  function path(values: number[]): string {
    return values.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join('')
  }

  function label(ms: number): string {
    const d = new Date(ms)
    return days
      ? d.toLocaleDateString(locale(), { month: 'numeric', day: 'numeric' })
      : d.toLocaleTimeString(locale(), { hour: '2-digit', minute: '2-digit', hour12: false })
  }

  // A running total is as of its span's end; an amount names its span's
  // ends when it covers more than a minute.
  function slot(point: number): string {
    const [fromBin, toBin] = binsOf(point)
    const from = start + fromBin * binMs
    const to = Math.min(start + toBin * binMs, now.getTime())
    const clock = (ms: number) => new Date(ms).toLocaleTimeString(locale(), { hour: '2-digit', minute: '2-digit', hour12: false })
    const text = cumulative ? clock(to) : to - from > 60_000 ? `${clock(from)}–${clock(to)}` : clock(from)
    return days ? `${label(cumulative ? to : from)} ${text}` : text
  }

  // A reset is a moment, so it keeps its time even on a chart of days.
  function resetLabel(ms: number): string {
    const time = new Date(ms).toLocaleTimeString(locale(), { hour: '2-digit', minute: '2-digit', hour12: false })
    return days ? `${label(ms)} ${time}` : time
  }

  const ticks = $derived([0, 0.5, 1].map((f) => ({ f, text: label(start + f * timeline.bins * binMs) })))

  // The bars: every model's tokens in each hour of the viewer's time zone, or
  // each day on a chart of days, on the line chart's time axis, so an hour or
  // day the chart starts or ends partway is a narrower bar.
  const barHeight = 40
  type Bar = { at: number; from: number; to: number; values: number[]; total: number }
  const bars = $derived.by(() => {
    const out: Bar[] = []
    for (let bin = 0; bin <= lastBin; bin++) {
      // shortcut: bars follow the viewer's local clock, so the hour a DST change repeats is one bar and a 30-minute bin across midnight (UTC+5:45) counts for the day it starts in; upgrade if exact totals matter there.
      const d = new Date(start + bin * binMs)
      if (days) d.setHours(0, 0, 0, 0)
      else d.setMinutes(0, 0, 0)
      if (out[out.length - 1]?.at !== d.getTime()) out.push({ at: d.getTime(), from: bin, to: bin, values: series.map(() => 0), total: 0 })
      const bar = out[out.length - 1]
      bar.to = bin
      series.forEach((s, i) => {
        bar.values[i] += s.perBin[bin]
        bar.total += s.perBin[bin]
      })
    }
    return out
  })
  const barTop = $derived(cleanTop(Math.max(0, ...bars.map((b) => b.total))))
  const barX = (bin: number) => (bin / timeline.bins) * width
  const barY = (v: number) => barHeight - (v / barTop) * (barHeight - 4) - 1

  let barHover = $state<number | null>(null)
  const hoveredBar = $derived(barHover === null ? null : (bars[barHover] ?? null))

  // An hour's bar names the span it covers, ending at now in the current
  // one; a day's bar names the day.
  const barSpan = (b: Bar) =>
    days ? label(b.at) : `${label(start + b.from * binMs)}–${label(Math.min(start + (b.to + 1) * binMs, now.getTime()))}`

  function onBarMove(e: PointerEvent) {
    const rect = (e.currentTarget as SVGElement).getBoundingClientRect()
    const bin = ((e.clientX - rect.left) / rect.width) * timeline.bins
    const i = bars.findIndex((b) => bin < b.to + 1)
    barHover = i < 0 ? bars.length - 1 : i
  }

  function onMove(e: PointerEvent) {
    const rect = (e.currentTarget as SVGElement).getBoundingClientRect()
    const bin = Math.floor(((e.clientX - rect.left) / rect.width) * timeline.bins)
    hover = Math.max(0, Math.min(lastPoint, Math.floor(bin / group)))
  }

  const people = $derived(members.filter((m) => (timeline.points ?? []).some((p) => p.person_id === m.person_id)))
</script>

<div class="chart">
  <div class="controls">
    {#if people.length > 1}
      <div class="chips" role="radiogroup" aria-label={t('usageOverTime')}>
        <button role="radio" aria-checked={person === ''} class:active={person === ''} onclick={() => (person = '')}>{t('everyone')}</button>
        {#each people as m (m.person_id)}
          <button role="radio" aria-checked={person === m.person_id} class:active={person === m.person_id} onclick={() => (person = m.person_id)}>{m.name}</button>
        {/each}
      </div>
    {/if}
    <div class="chips toggle">
      <button aria-pressed={cumulative} class:active={cumulative} onclick={() => (cumulative = !cumulative)}>{t('cumulative')}</button>
    </div>
  </div>

  <span class="axis num">{series.length ? tokens(top) : '0'}</span>
  <div class="plot" bind:clientWidth={width}>
    {#if width > 0}
      <svg {width} {height} role="img" aria-label={t('usageOverTime')} onpointermove={onMove} onpointerleave={() => (hover = null)}>
        <!-- Styled inline, not by class: Copy as image re-renders the chart
             from its elements and would miss class-scoped SVG styles. -->
        <line x1="0" x2={width} y1={y(top)} y2={y(top)} style:stroke="var(--line)" style:stroke-width="1" />
        <line x1="0" x2={width} y1={y(0)} y2={y(0)} style:stroke="var(--line)" style:stroke-width="1" />
        {#each resets as r, i (i)}
          <line x1={r.x} x2={r.x} y1="0" y2={height} style:stroke="var(--muted)" style:stroke-width="1" style:stroke-dasharray="3 3" />
        {/each}
        {#if hover !== null}
          <line x1={x(hover)} x2={x(hover)} y1="0" y2={height} style:stroke="var(--muted)" style:stroke-width="1" />
        {/if}
        {#each series as s (s.key)}
          <path
            d={path(s.values)}
            style:fill="none"
            style:stroke={color(s.model, s.thirdParty, s.gateway)}
            style:stroke-width="2"
            style:stroke-linejoin="round"
            style:stroke-linecap="round"
            style:stroke-dasharray={s.thirdParty ? '4 3' : null}
          />
        {/each}
      </svg>
    {/if}
    {#each resets as r, i (i)}
      <!-- Left of its line, unless too close to the chart's left edge. -->
      <span class="reset-label num" class:right={r.x < 40} style:left="{r.x}px">{r.text}</span>
    {/each}
    {#if hover !== null}
      <div class="tip" class:flip={x(hover) > width / 2} style:left="{x(hover)}px">
        <div class="muted">{slot(hover)}</div>
        {@render tipRows(series.map((s) => s.values[hover!]))}
      </div>
    {/if}
  </div>
  <div class="ticks muted num">
    {#each ticks as tick (tick.f)}<span>{tick.text}</span>{/each}
  </div>

  <span class="axis num">{series.length ? tokens(barTop) : '0'}</span>
  <div class="plot" style:height="{barHeight}px">
    {#if width > 0}
      <svg {width} height={barHeight} role="img" aria-label={t(days ? 'usagePerDay' : 'usagePerHour')} onpointermove={onBarMove} onpointerleave={() => (barHover = null)}>
        {#if hoveredBar}
          <rect x={barX(hoveredBar.from)} y="0" width={barX(hoveredBar.to + 1) - barX(hoveredBar.from)} height={barHeight} style:fill="var(--track)" />
        {/if}
        <line x1="0" x2={width} y1={barY(barTop)} y2={barY(barTop)} style:stroke="var(--line)" style:stroke-width="1" />
        <line x1="0" x2={width} y1={barY(0)} y2={barY(0)} style:stroke="var(--line)" style:stroke-width="1" />
        {#each bars as b}
          {#each series as s, i}
            {#if b.values[i] > 0}
              {@const below = b.values.slice(0, i).reduce((sum, v) => sum + v, 0)}
              <rect
                x={barX(b.from) + 1}
                y={barY(below + b.values[i])}
                width={Math.max(1, barX(b.to + 1) - barX(b.from) - 2)}
                height={barY(below) - barY(below + b.values[i])}
                style:fill={color(s.model, s.thirdParty, s.gateway)}
                style:opacity={s.thirdParty ? 0.5 : null}
              />
            {/if}
          {/each}
        {/each}
      </svg>
    {/if}
    {#if hoveredBar}
      <div class="tip under">
        <div class="tip-head"><span class="muted">{barSpan(hoveredBar)}</span><strong class="num">{tokens(hoveredBar.total)}</strong></div>
        {@render tipRows(hoveredBar.values)}
      </div>
    {/if}
  </div>
</div>

<!-- Each model's amount, in the order of the series, for a tip. -->
{#snippet tipRows(values: number[])}
  {#each series as s, i (s.key)}
    {#if values[i] > 0}
      {@const shown = modelName(s.model, s.gateway)}
      <div class="tip-row">
        <i class:third={s.thirdParty} style:color={color(s.model, s.thirdParty, s.gateway)}></i>
        <strong class="num">{tokens(values[i])}</strong>
        <span class="muted">{shown.name}</span>
        {#if shown.via}<span class="via">{shown.via}</span>{/if}
      </div>
    {/if}
  {/each}
{/snippet}

<style>
  .chart { display: grid; gap: 6px; }
  .controls { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 4px 8px; }
  .chips { display: flex; flex-wrap: wrap; gap: 4px; }
  .toggle { margin-left: auto; }
  .chips button {
    font-size: 11.5px;
    padding: 1px 8px;
    border-radius: 999px;
    color: var(--muted);
  }
  .chips button.active { color: var(--text); border-color: var(--accent); }
  .plot { position: relative; height: 96px; min-width: 0; }
  /* Positioned so the drawn width never holds the card open when the
     popup narrows; the width comes from the plot, not the other way. */
  svg { position: absolute; inset: 0; display: block; overflow: visible; touch-action: none; }
  .reset-label {
    position: absolute;
    top: 2px;
    transform: translateX(calc(-100% - 4px));
    font-size: 10.5px;
    color: var(--muted);
    pointer-events: none;
    white-space: nowrap;
  }
  .reset-label.right { transform: translateX(4px); }
  .axis { font-size: 10.5px; color: var(--muted); margin-bottom: -4px; }
  .ticks { display: flex; justify-content: space-between; font-size: 10.5px; }
  .tip {
    position: absolute;
    top: 0;
    transform: translateX(8px);
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 6px;
    padding: 5px 7px;
    font-size: 11px;
    display: grid;
    gap: 2px;
    pointer-events: none;
    white-space: nowrap;
    z-index: 1;
    box-shadow: 0 2px 8px rgb(0 0 0 / .15);
    backdrop-filter: blur(12px);
  }
  .tip.flip { transform: translateX(calc(-100% - 8px)); }
  /* The bars' tip spans the plot below it, so it never covers a bar or leaves the popup. */
  .tip.under { top: calc(100% + 4px); left: 0; right: 0; transform: none; }
  .tip-head { display: flex; justify-content: space-between; gap: 12px; }
  .tip-row { display: flex; align-items: center; gap: 6px; }
  .tip-row .via { font-size: 9.5px; letter-spacing: .04em; color: var(--muted); border: 1px solid var(--line); border-radius: 3px; padding: 0 3px; }
  .tip-row i { width: 10px; height: 2px; border-radius: 1px; flex: none; background: currentColor; }
  /* Third-party models never count against the quota: dashed, like their
     list's swatches. */
  .tip-row i.third { background: repeating-linear-gradient(90deg, currentColor 0 3px, transparent 3px 5px); }
</style>
