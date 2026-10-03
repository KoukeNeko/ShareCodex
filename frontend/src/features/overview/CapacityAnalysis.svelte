<script lang="ts">
  import { Desktop, errorMessage, type CapacityReport } from '../../lib/api'
  import { tokens, usd } from '../../lib/format'
  import { t } from '../../lib/i18n.svelte'

  let { accountId, accountLabel }: { accountId: string; accountLabel: string } = $props()

  const periods = ['24h', '7d', '30d']
  const periodLabel = (id: string) => (id === '24h' ? t('hours', { count: 24 }) : t('days', { count: parseInt(id) }))

  let period = $state('7d')
  let ratio = $state(4.0)
  let report = $state<CapacityReport | null>(null)
  let error = $state('')

  $effect(() => {
    const id = accountId
    const span = period
    const r = ratio
    let stale = false
    Desktop.AccountCapacity(id, span, r)
      .then((rep) => {
        if (stale) return
        report = rep
        error = ''
      })
      .catch((err) => {
        if (!stale) error = errorMessage(err)
      })
    return () => (stale = true)
  })
</script>

<div class="controls">
  <div class="periods">
    {#each periods as p (p)}
      <button class:active={period === p} onclick={() => (period = p)}>{periodLabel(p)}</button>
    {/each}
  </div>
</div>

{#if error}
  <p class="error">{error}</p>
{:else if !report}
  <p class="muted">{t('loading')}</p>
{:else}
  <section class="card stats">
    <div class="stat-col">
      <span class="muted small">{t('totalTokens')}</span>
      <strong class="stat-num">{tokens(report.total_tokens)}</strong>
    </div>
    <div class="stat-col">
      <span class="muted small">{t('rollingPeak')}</span>
      <strong class="stat-num">{usd(report.combined_peak_cost)}</strong>
    </div>
    <div class="stat-col">
      <span class="muted small">P50 / P95 (5h)</span>
      <strong class="stat-num">{usd(report.combined_p50_cost)} / {usd(report.combined_p95_cost)}</strong>
    </div>
    <div class="stat-col">
      <span class="muted small">5h 100% / Weekly 100%</span>
      <strong class="stat-num">{report.saturation_stats.five_hour_saturated_count} / {report.saturation_stats.weekly_saturated_count}</strong>
    </div>
  </section>

  <section class="card">
    <h2 class="muted small">{t('scenarios')} (5h {report.ratio}x)</h2>
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>{t('scenarios')}</th>
            <th class="num">Cap.</th>
            <th class="num">Peak</th>
            <th class="num">Peak %</th>
            <th class="num">{t('headroom')}</th>
            <th class="status-col">Fit</th>
          </tr>
        </thead>
        <tbody>
          {#each report.scenarios as s (s.name)}
            <tr>
              <td><strong>{s.label}</strong></td>
              <td class="num">{usd(s.capacity)}</td>
              <td class="num">{usd(s.peak_demand)}</td>
              <td class="num">{s.peak_percent}%</td>
              <td class="num">{s.headroom_percent}%</td>
              <td class="status-col">
                <span class="badge" class:fit={s.fit === 'fit'} class:borderline={s.fit === 'borderline'} class:exceeded={s.fit === 'exceeded'}>
                  {s.fit}
                </span>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>

  <section class="card">
    <h2 class="muted small">{t('member')} Breakdown & Separate 5x</h2>
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>{t('member')}</th>
            <th class="num">{t('tokens')}</th>
            <th class="num">Peak 5h</th>
            <th class="num">Sep 5x %</th>
            <th class="num">{t('headroom')}</th>
            <th class="status-col">Fit</th>
          </tr>
        </thead>
        <tbody>
          {#each report.members as m (m.person_id)}
            <tr>
              <td><strong>{m.name}</strong></td>
              <td class="num">{tokens(m.tokens)}</td>
              <td class="num">{usd(m.peak_rolling_cost)}</td>
              {#if m.scenarios && m.scenarios.length > 0}
                <td class="num">{m.scenarios[0].peak_percent}%</td>
                <td class="num">{m.scenarios[0].headroom_percent}%</td>
                <td class="status-col">
                  <span class="badge" class:fit={m.scenarios[0].fit === 'fit'} class:borderline={m.scenarios[0].fit === 'borderline'} class:exceeded={m.scenarios[0].fit === 'exceeded'}>
                    {m.scenarios[0].fit}
                  </span>
                </td>
              {:else}
                <td colspan="3" class="muted">—</td>
              {/if}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>

  {#if report.limit_events && report.limit_events.length > 0}
    <section class="card">
      <h2 class="muted small">Limit & Rejection Events</h2>
      <ul class="event-list">
        {#each report.limit_events as l (l.occurred_at)}
          <li>
            <span><span class="badge">{l.kind}</span> {l.person_name ?? '—'}</span>
            <span class="muted small">{l.source}</span>
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if report.data_notes && report.data_notes.length > 0}
    <section class="card notes">
      <h2 class="muted small">Methodology & Assumptions</h2>
      <ul>
        {#each report.data_notes as note}
          <li class="muted small">{note}</li>
        {/each}
      </ul>
    </section>
  {/if}
{/if}

<style>
  .controls { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
  .periods { display: flex; gap: 4px; }
  .periods button {
    font-size: 11.5px;
    padding: 1px 8px;
    border-radius: 999px;
    color: var(--muted);
  }
  .periods button.active { color: var(--text); border-color: var(--accent); }
  .card {
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 12px 14px;
    margin-bottom: 8px;
  }
  .stats {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 12px;
  }
  .stat-col { display: flex; flex-direction: column; gap: 2px; }
  .stat-num { font-size: 16px; font-weight: 600; color: var(--text); }
  .small { font-size: 11px; }
  .muted { color: var(--muted); }
  h2 { margin: 0 0 8px 0; font-weight: normal; }
  .table-wrap { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 11.5px; }
  th, td { padding: 4px 6px; text-align: left; }
  th.num, td.num { text-align: right; }
  th.status-col, td.status-col { text-align: right; width: 65px; }
  tbody tr:not(:last-child) { border-bottom: 1px solid var(--line); }
  .badge {
    font-size: 10px;
    text-transform: capitalize;
    letter-spacing: .02em;
    padding: 1px 5px;
    border-radius: 4px;
    background: var(--line);
    color: var(--muted);
  }
  .badge.fit { color: var(--accent); background: var(--accent-soft); }
  .badge.borderline { color: #eab308; background: rgba(234, 179, 8, 0.15); }
  .badge.exceeded { color: #ef4444; background: rgba(239, 68, 68, 0.15); }
  .event-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
  .event-list li { display: flex; justify-content: space-between; align-items: baseline; font-size: 11.5px; }
  .notes ul { margin: 0; padding-left: 16px; }
  .notes li { margin-bottom: 4px; }
</style>
