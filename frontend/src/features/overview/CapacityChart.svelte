<script lang="ts">
  import type { CapacityReport, CapacityScenario } from '../../lib/api'
  import { clock, dateTime, percent } from '../../lib/format'
  import { locale, t } from '../../lib/i18n.svelte'

  // A scenario's predicted use of one limit over the report's period: a step
  // line for each limit replayed (the members' own, in the model colors, or
  // the shared one), the provider's readings as dots, the 100% line, the
  // borderline and the limits Claude Code logged.
  let { report, scenario, bucket }: { report: CapacityReport; scenario: CapacityScenario; bucket: 'five_hour' | 'weekly' } = $props()

  const height = 96
  const slices = 60
  let width = $state(0)
  let hover = $state<number | null>(null)

  const start = $derived(new Date(report.start).getTime() / 1000)
  const span = $derived(new Date(report.end).getTime() / 1000 - start)

  type Line = { name: string; color: string; points: number[][] }
  const lines = $derived.by(() => {
    const out: Line[] = []
    const add = (name: string, color: string, result: { basis: string; series?: number[][] | null }) => {
      if (result.basis !== 'insufficient') out.push({ name, color, points: result.series ?? [] })
    }
    if (scenario.shared) add(t('predicted'), 'var(--accent)', scenario[bucket])
    ;(scenario.members ?? []).forEach((m, i) => add(m.name, i < 4 ? `var(--model-${i + 1})` : 'var(--model-other)', m[bucket]))
    return out
  })
  const readings = $derived(bucket === 'five_hour' ? (report.readings ?? []) : [])
  const limitKind = $derived(bucket === 'five_hour' ? '5h' : 'weekly')
  const marks = $derived(
    (report.limit_hits ?? []).filter((h) => h.kind === limitKind).map((h) => new Date(h.at).getTime() / 1000).filter((s) => s >= start && s < start + span),
  )

  // The axis reaches 100%, or the highest use past it.
  const top = $derived(Math.max(100, Math.ceil(Math.max(0, ...lines.flatMap((l) => l.points.map((p) => p[1]))) / 10) * 10))
  const x = (sec: number) => Math.min(1, Math.max(0, (sec - start) / span)) * width
  const y = (v: number) => height - (v / top) * (height - 4) - 1

  // Each point holds until the next, as the replay counts it.
  const path = (points: number[][]) => points.map(([s, v], i) => (i ? `H${x(s).toFixed(1)}V${y(v).toFixed(1)}` : `M${x(s).toFixed(1)},${y(v).toFixed(1)}`)).join('')

  // The span each plan was in effect, shaded in turn where the period saw more than one.
  const plans = $derived.by(() => {
    const end = start + span
    const spans = (report.plans ?? [])
      .map((p) => ({ plan: p.plan_type, from: Math.max(start, new Date(p.effective_at).getTime() / 1000), to: Math.min(end, p.ended_at ? new Date(p.ended_at).getTime() / 1000 : end) }))
      .filter((p) => p.to > p.from)
      .sort((a, b) => a.from - b.from)
    return spans.length > 1 ? spans : []
  })

  // The most use a step series held in [from, to).
  function held(points: number[][], from: number, to: number): number {
    let most = [...points].reverse().find((p) => p[0] <= from)?.[1] ?? 0
    for (const [s, v] of points) if (s > from && s < to) most = Math.max(most, v)
    return most
  }

  const tip = $derived.by(() => {
    if (hover === null) return null
    const from = start + (hover / slices) * span
    const to = from + span / slices
    const rows = lines.map((l) => ({ ...l, value: held(l.points, from, to) })).filter((r) => r.value > 0)
    const seen = readings.filter(([s]) => s >= from && s < to).map((r) => r[1])
    if (seen.length) rows.push({ name: t('actual'), color: 'var(--text)', points: [], value: Math.max(...seen) })
    return { at: new Date(from * 1000).toISOString(), rows, mid: ((hover + 0.5) / slices) * width }
  })

  const label = (sec: number) => {
    const d = new Date(sec * 1000)
    return span > 86400 ? d.toLocaleDateString(locale(), { month: 'numeric', day: 'numeric' }) : clock(d.toISOString())
  }
  const ticks = $derived([0, 0.5, 1].map((f) => ({ f, text: label(start + f * span) })))

  function onMove(e: PointerEvent) {
    const rect = (e.currentTarget as SVGElement).getBoundingClientRect()
    hover = Math.max(0, Math.min(slices - 1, Math.floor(((e.clientX - rect.left) / rect.width) * slices)))
  }
</script>

{#if lines.length}
  <div class="chart">
    <span class="axis num">{top}%</span>
    <div class="plot" bind:clientWidth={width}>
      {#if width > 0}
        <svg {width} {height} role="img" aria-label={t(bucket === 'weekly' ? 'weekly' : 'fiveHour')} onpointermove={onMove} onpointerleave={() => (hover = null)}>
          <!-- Styled inline, not by class: Copy as image re-renders the chart
               from its elements and would miss class-scoped SVG styles. -->
          {#each plans as p, i (p.from)}
            {#if i % 2}<rect x={x(p.from)} y="0" width={x(p.to) - x(p.from)} {height} style:fill="var(--track)" />{/if}
          {/each}
          <line x1="0" x2={width} y1={y(0)} y2={y(0)} style:stroke="var(--line)" style:stroke-width="1" />
          <line x1="0" x2={width} y1={y(report.borderline_percent)} y2={y(report.borderline_percent)} style:stroke="var(--warn)" style:stroke-width="1" style:stroke-dasharray="2 4" style:opacity="0.6" />
          <line x1="0" x2={width} y1={y(100)} y2={y(100)} style:stroke="var(--danger)" style:stroke-width="1" style:opacity="0.5" />
          {#each marks as s (s)}
            <line x1={x(s)} x2={x(s)} y1="0" y2={height} style:stroke="var(--danger)" style:stroke-width="2" />
          {/each}
          {#if hover !== null}
            <line x1={tip?.mid} x2={tip?.mid} y1="0" y2={height} style:stroke="var(--muted)" style:stroke-width="1" />
          {/if}
          {#each lines as l (l.name)}
            <path d={path(l.points)} style:fill="none" style:stroke={l.color} style:stroke-width="2" style:stroke-linejoin="round" />
          {/each}
          {#each readings as [s, v] (s)}
            <circle cx={x(s)} cy={y(v)} r="2" style:fill="var(--text)" style:opacity="0.7" />
          {/each}
        </svg>
      {/if}
      {#each plans as p, i (p.from)}
        <span class="plan-label num" style:left="{x(p.from)}px">{p.plan}</span>
      {/each}
      {#if tip}
        <div class="tip" class:flip={tip.mid > width / 2} style:left="{tip.mid}px">
          <div class="muted">{dateTime(tip.at)}</div>
          {#each tip.rows as r (r.name)}
            <div class="tip-row">
              <i style:color={r.color}></i>
              <strong class="num">{percent(r.value)}</strong>
              <span class="muted">{r.name}</span>
            </div>
          {/each}
        </div>
      {/if}
    </div>
    <div class="ticks muted num">
      {#each ticks as tick (tick.f)}<span>{tick.text}</span>{/each}
    </div>
    <div class="legend muted">
      {#each lines as l (l.name)}<span><i class="swatch" style:background={l.color}></i>{l.name}</span>{/each}
      {#if readings.length}<span><i class="swatch dot"></i>{t('actual')}</span>{/if}
      {#if marks.length}<span><i class="swatch mark"></i>{t('limitLogged')}</span>{/if}
    </div>
  </div>
{/if}

<style>
  .chart { display: grid; gap: 6px; }
  .plot { position: relative; height: 96px; min-width: 0; }
  /* Positioned so the drawn width never holds the card open when the
     popup narrows; the width comes from the plot, not the other way. */
  svg { position: absolute; inset: 0; display: block; overflow: visible; touch-action: none; }
  .axis { font-size: 10.5px; color: var(--muted); margin-bottom: -4px; }
  .ticks { display: flex; justify-content: space-between; font-size: 10.5px; }
  .plan-label { position: absolute; top: 2px; transform: translateX(4px); font-size: 10.5px; color: var(--muted); pointer-events: none; white-space: nowrap; }
  .legend { display: flex; flex-wrap: wrap; gap: 2px 12px; font-size: 11px; }
  .legend span { display: inline-flex; align-items: center; gap: 5px; }
  .swatch { width: 8px; height: 8px; border-radius: 2px; flex: none; }
  .swatch.dot { border-radius: 50%; background: var(--text); opacity: .7; }
  .swatch.mark { width: 2px; height: 10px; border-radius: 0; background: var(--danger); }
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
  .tip-row i { width: 10px; height: 2px; border-radius: 1px; flex: none; background: currentColor; }
</style>
