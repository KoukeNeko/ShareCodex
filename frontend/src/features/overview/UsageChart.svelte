<script lang="ts">
  import type { MemberShare, Timeline } from '../../lib/api'
  import { tokens } from '../../lib/format'
  import { locale, t } from '../../lib/i18n.svelte'

  // Tokens over one quota window, a line per model, for everyone or one
  // member. Models keep the colors and order of the card's Models list,
  // which is the chart's legend.
  let { timeline, models, color, members, now }: {
    timeline: Timeline
    models: string[]
    color: (model: string) => string
    members: MemberShare[]
    now: Date
  } = $props()

  let person = $state('')
  let width = $state(0)
  let hover = $state<number | null>(null)

  const height = 96
  const start = $derived(new Date(timeline.start).getTime())
  const binMs = $derived(timeline.bin_minutes * 60_000)
  // Bins after now have not happened yet, so their lines stop at now.
  const lastBin = $derived(Math.min(timeline.bins - 1, Math.max(0, Math.floor((now.getTime() - start) / binMs))))

  const series = $derived.by(() => {
    const byModel = new Map<string, number[]>()
    for (const p of timeline.points ?? []) {
      if (person && p.person_id !== person) continue
      if (p.bin < 0 || p.bin > lastBin) continue
      let values = byModel.get(p.model)
      if (!values) byModel.set(p.model, (values = new Array(lastBin + 1).fill(0)))
      values[p.bin] += p.tokens
    }
    return models.filter((m) => byModel.has(m)).map((m) => ({ model: m, values: byModel.get(m)! }))
  })

  // A clean top for the axis: 1, 2 or 5 times a power of ten.
  const top = $derived.by(() => {
    const max = Math.max(0, ...series.flatMap((s) => s.values))
    if (max === 0) return 1
    const step = 10 ** Math.floor(Math.log10(max))
    return [1, 2, 5, 10].map((k) => k * step).find((v) => v >= max)!
  })

  const x = (bin: number) => ((bin + 0.5) / timeline.bins) * width
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
    return timeline.bins * timeline.bin_minutes > 1440
      ? d.toLocaleDateString(locale(), { month: 'numeric', day: 'numeric' })
      : d.toLocaleTimeString(locale(), { hour: '2-digit', minute: '2-digit', hour12: false })
  }

  // A bin covers a span, so its tooltip names both ends.
  function slot(bin: number): string {
    const from = start + bin * binMs
    const clock = (ms: number) => new Date(ms).toLocaleTimeString(locale(), { hour: '2-digit', minute: '2-digit', hour12: false })
    return timeline.bins * timeline.bin_minutes > 1440 ? `${label(from)} ${clock(from)}–${clock(from + binMs)}` : `${clock(from)}–${clock(from + binMs)}`
  }

  // A reset is a moment, so it keeps its time even on a chart of days.
  function resetLabel(ms: number): string {
    const time = new Date(ms).toLocaleTimeString(locale(), { hour: '2-digit', minute: '2-digit', hour12: false })
    return timeline.bins * timeline.bin_minutes > 1440 ? `${label(ms)} ${time}` : time
  }

  const ticks = $derived([0, 0.5, 1].map((f) => ({ f, text: label(start + f * timeline.bins * binMs) })))

  function onMove(e: PointerEvent) {
    const rect = (e.currentTarget as SVGElement).getBoundingClientRect()
    const bin = Math.floor(((e.clientX - rect.left) / rect.width) * timeline.bins)
    hover = Math.max(0, Math.min(lastBin, bin))
  }

  const people = $derived(members.filter((m) => (timeline.points ?? []).some((p) => p.person_id === m.person_id)))
</script>

<div class="chart">
  {#if people.length > 1}
    <div class="people" role="radiogroup" aria-label={t('usageOverTime')}>
      <button role="radio" aria-checked={person === ''} class:active={person === ''} onclick={() => (person = '')}>{t('everyone')}</button>
      {#each people as m (m.person_id)}
        <button role="radio" aria-checked={person === m.person_id} class:active={person === m.person_id} onclick={() => (person = m.person_id)}>{m.name}</button>
      {/each}
    </div>
  {/if}

  <span class="axis num">{tokens(top)}</span>
  <div class="plot" bind:clientWidth={width}>
    {#if width > 0}
      <svg {width} {height} role="img" aria-label={t('usageOverTime')} onpointermove={onMove} onpointerleave={() => (hover = null)}>
        <line class="grid" x1="0" x2={width} y1={y(top)} y2={y(top)} />
        <line class="grid" x1="0" x2={width} y1={y(0)} y2={y(0)} />
        {#each resets as r, i (i)}
          <line class="reset" x1={r.x} x2={r.x} y1="0" y2={height} />
        {/each}
        {#if hover !== null}
          <line class="cross" x1={x(hover)} x2={x(hover)} y1="0" y2={height} />
        {/if}
        {#each series as s (s.model)}
          <path d={path(s.values)} style:stroke={color(s.model)} />
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
        {#each series as s (s.model)}
          <div class="tip-row">
            <i style:background={color(s.model)}></i>
            <strong class="num">{tokens(s.values[hover])}</strong>
            <span class="muted">{s.model}</span>
          </div>
        {/each}
      </div>
    {/if}
  </div>
  <div class="ticks muted num">
    {#each ticks as tick (tick.f)}<span>{tick.text}</span>{/each}
  </div>
</div>

<style>
  .chart { display: grid; gap: 6px; }
  .people { display: flex; flex-wrap: wrap; gap: 4px; }
  .people button {
    font-size: 11.5px;
    padding: 1px 8px;
    border-radius: 999px;
    color: var(--muted);
  }
  .people button.active { color: var(--text); border-color: var(--accent); }
  .plot { position: relative; height: 96px; min-width: 0; }
  /* Positioned so the drawn width never holds the card open when the
     popup narrows; the width comes from the plot, not the other way. */
  svg { position: absolute; inset: 0; display: block; overflow: visible; touch-action: none; }
  .grid { stroke: var(--line); stroke-width: 1; }
  .cross { stroke: var(--muted); stroke-width: 1; }
  .reset { stroke: var(--muted); stroke-width: 1; stroke-dasharray: 3 3; }
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
  path { fill: none; stroke-width: 2; stroke-linejoin: round; stroke-linecap: round; }
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
  .tip-row { display: flex; align-items: center; gap: 6px; }
  .tip-row i { width: 10px; height: 2px; border-radius: 1px; flex: none; }
</style>
