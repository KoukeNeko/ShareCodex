<script lang="ts">
  import { Desktop, errorMessage, type CapacityReport, type FiveHourResult, type WeeklyResult } from '../../lib/api'
  import { bucketName, dateTime, percent, tokens, usd } from '../../lib/format'
  import { t, type MessageKey } from '../../lib/i18n.svelte'
  import DateRangePicker from '../../components/DateRangePicker.svelte'
  import CapacityChart from './CapacityChart.svelte'

  let { accountId }: { accountId: string } = $props()

  const periods = ['24h', '7d', '30d']
  const periodLabel = (id: string) => (id === '24h' ? t('hours', { count: 24 }) : t('days', { count: parseInt(id) }))

  // The report carries codes; each has its own text.
  const basisText: Record<string, MessageKey> = { measured: 'basisMeasured', derived: 'basisDerived', insufficient: 'basisInsufficient' }
  const fitText: Record<string, MessageKey> = { safe: 'fitSafe', borderline: 'fitBorderline', over: 'fitOver' }
  const excludedText: Record<string, MessageKey> = {
    unknown_plan: 'excludedUnknownPlan',
    plan_changed: 'excludedPlanChanged',
    no_sample: 'excludedNoSample',
    low_cost: 'excludedLowCost',
  }
  const scenarioText: Record<string, MessageKey> = {
    shared_max5: 'scenarioSharedMax5',
    separate_max5: 'scenarioSeparateMax5',
    shared_max20: 'scenarioSharedMax20',
  }
  const kindText: Record<string, MessageKey> = {
    '5h': 'limitKind5h',
    weekly: 'limitKindWeekly',
    'model-specific': 'limitKindModelSpecific',
    overload: 'limitKindOverload',
    provider429: 'limitKindProvider429',
    auth: 'limitKindAuth',
    unknown: 'limitKindUnknown',
  }
  const evidenceText: Record<string, MessageKey> = {
    five_hour: 'limitEvidenceFiveHour',
    seven_day: 'limitEvidenceSevenDay',
    session_limit: 'limitEvidenceSessionLimit',
    weekly_limit: 'limitEvidenceWeeklyLimit',
    extra_usage: 'limitEvidenceExtraUsage',
    gateway: 'limitEvidenceGateway',
    overloaded: 'limitEvidenceOverloaded',
    auth: 'limitEvidenceAuth',
  }
  const reasonText: Record<string, MessageKey> = { unsupported_provider: 'reasonUnsupportedProvider' }
  const label = (texts: Record<string, MessageKey>, code: string) => (code in texts ? t(texts[code]) : code)

  const rate = (v: number) => v.toPrecision(3).replace(/\.?0+$/, '')

  let period = $state('7d')
  // A range in use replaces the period until a period is picked again.
  let range = $state<{ from: string; to: string } | null>(null)
  const pick = (p: string) => {
    period = p
    range = null
  }
  let report = $state<CapacityReport | null>(null)
  let error = $state('')

  // Fetches again when the span changes; the last answer stays up meanwhile,
  // and an answer to an earlier span is dropped.
  $effect(() => {
    const id = accountId
    const span = period
    const custom = range
    let stale = false
    Desktop.AccountCapacity(id, span, custom?.from ?? '', custom?.to ?? '')
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

{#snippet tag(kind: string, text: string)}
  <span class="tag {kind}">{text}</span>
{/snippet}

{#snippet planName(plan: string)}
  {#if plan}{plan}{:else}<span class="muted">{t('unknownPlan')}</span>{/if}
{/snippet}

{#snippet fiveHourBlock(r: FiveHourResult)}
  <div class="block-head">
    <span class="muted small">{t('fiveHour')}</span>
    {@render tag(r.basis, label(basisText, r.basis))}
    {#if r.basis !== 'insufficient'}
      <span class="num">{usd(r.capacity_usd)}</span>
      {@render tag(r.fit ?? '', label(fitText, r.fit ?? ''))}
    {/if}
  </div>
  {#if r.basis !== 'insufficient'}
    <div class="figures">
      <span class="muted">{t('limitHits')}</span><span class="num">{r.hit_windows} / {r.windows}</span>
      {#if r.first_hit}<span class="muted">{t('firstHit')}</span><span class="num">{dateTime(r.first_hit)}</span>{/if}
      <span class="muted">{t('peak')}</span><span class="num">{percent(r.peak_percent)}</span>
      <span class="muted">{t('blocked')}</span><span class="num">{percent(r.blocked_percent)}</span>
      <span class="muted">{t('rollingPeak')}</span><span class="num">{percent(r.rolling_peak_percent)}</span>
      <span class="muted">P50 / P95</span><span class="num">{percent(r.rolling_p50_percent)} / {percent(r.rolling_p95_percent)}</span>
    </div>
  {/if}
{/snippet}

{#snippet weeklyBlock(r: WeeklyResult)}
  <div class="block-head">
    <span class="muted small">{t('weekly')}</span>
    {@render tag(r.basis, label(basisText, r.basis))}
    {#if r.basis !== 'insufficient'}
      <span class="num">{usd(r.capacity_usd)}</span>
      {@render tag(r.fit ?? '', label(fitText, r.fit ?? ''))}
    {/if}
  </div>
  {#if r.basis !== 'insufficient'}
    <div class="figures">
      <span class="muted">{t('limitHits')}</span><span class="num">{r.hit_windows} / {r.windows?.length ?? 0}</span>
      <span class="muted">{t('peak')}</span><span class="num">{percent(r.peak_percent)}</span>
    </div>
  {/if}
{/snippet}

<div class="periods">
  {#each periods as p (p)}
    <button class:active={!range && period === p} onclick={() => pick(p)}>{periodLabel(p)}</button>
  {/each}
  <DateRangePicker {range} onapply={(from, to) => (range = { from, to })} />
</div>

{#if error}
  <p class="error">{error}</p>
{:else if !report}
  <p class="muted">{t('loading')}</p>
{:else}
  <section class="card stats">
    <div class="stat-col">
      <span class="muted small">{t('totalTokens')}</span>
      <strong class="stat-num">{tokens(report.demand.tokens)}</strong>
    </div>
    <div class="stat-col">
      <span class="muted small">{t('estCost')}</span>
      <strong class="stat-num">{usd(report.demand.cost_usd)}</strong>
    </div>
    <div class="stat-col">
      <span class="muted small">{t('requests')}</span>
      <strong class="stat-num">{tokens(report.demand.requests)}</strong>
    </div>
    <div class="stat-col">
      <span class="muted small">{t('rollingPeak')}</span>
      <strong class="stat-num">{usd(report.demand.rolling_peak_usd)}</strong>
    </div>
  </section>

  {#if (report.members ?? []).length > 0}
    <section class="card">
      <h2 class="muted small">{t('members')}</h2>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>{t('member')}</th>
              <th class="num">{t('estCost')}</th>
              <th class="num">{t('rollingPeak')}</th>
            </tr>
          </thead>
          <tbody>
            {#each report.members as m (m.person_id)}
              <tr>
                <td>{m.name}</td>
                <td class="num">{usd(m.cost_usd)}</td>
                <td class="num">{usd(m.rolling_peak_usd)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
  {/if}

  {#if report.reason}
    <p class="muted">{label(reasonText, report.reason)}</p>
  {:else}
    <section class="card">
      <h2 class="muted small">{t('calibration')}</h2>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>{t('limit')}</th>
              <th>{t('plan')}</th>
              <th class="num">{t('samples')}</th>
              <th class="num">{t('rate')}</th>
              <th class="num">{t('capacityColumn')}</th>
            </tr>
          </thead>
          <tbody>
            {#each report.calibration ?? [] as c (c.bucket + c.plan)}
              <tr>
                <td>{bucketName(c.bucket, 0)}</td>
                <td>{c.plan}</td>
                <td class="num">{c.samples}</td>
                <td class="num">{rate(c.rate)}</td>
                <td class="num">{usd(c.capacity_usd)}</td>
              </tr>
            {:else}
              <tr><td colspan="5" class="muted">—</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#each report.multiples ?? [] as m (m.bucket)}
        <p class="muted small">
          {bucketName(m.bucket, 0)} · Max 20x / Max 5x · {t('observedMultiple')} {m.observed.toFixed(2)}{#if m.official} · {t('officialMultiple')} {m.official}{/if}
        </p>
      {/each}
    </section>

    <section class="card">
      <h2 class="muted small">{t('observedWindows')}</h2>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>{t('start')}</th>
              <th>{t('plan')}</th>
              <th class="num">{t('actual')}</th>
              <th class="num">{t('predicted')}</th>
              <th class="num">{t('estCost')}</th>
            </tr>
          </thead>
          <tbody>
            {#each report.windows ?? [] as w (w.start)}
              <tr>
                <td>{dateTime(w.start)}</td>
                <td>{@render planName(w.plan)}</td>
                <td class="num" title={w.first_full ? `${t('atLimit')} ${dateTime(w.first_full)}` : ''}>{percent(w.max_percent)}</td>
                <td class="num">{w.predicted_percent == null ? '—' : percent(w.predicted_percent)}</td>
                <td class="num">{usd(w.cost_usd)}</td>
              </tr>
              {#if w.excluded || w.limit_hit}
                <tr class="sub">
                  <td colspan="5">
                    {#if w.limit_hit}{@render tag('over', t('limitLogged'))}{/if}
                    {#if w.excluded}{@render tag('off', label(excludedText, w.excluded))}{/if}
                  </td>
                </tr>
              {/if}
            {:else}
              <tr><td colspan="5" class="muted">—</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>

    <section class="card">
      <h2 class="muted small">{t('saturation')}</h2>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>{t('limit')}</th>
              <th>{t('plan')}</th>
              <th class="num">{t('windows')}</th>
              <th class="num">{t('atLimit')}</th>
              <th class="num">{t('loggedLimits')}</th>
            </tr>
          </thead>
          <tbody>
            {#each report.saturation ?? [] as s (s.bucket + s.plan)}
              <tr>
                <td>{bucketName(s.bucket, 0)}</td>
                <td>{@render planName(s.plan)}</td>
                <td class="num">{s.windows}</td>
                <td class="num">{s.saturated}</td>
                <td class="num">{s.hits}</td>
              </tr>
            {:else}
              <tr><td colspan="5" class="muted">—</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>

    <section class="card">
      <h2 class="muted small">{t('loggedLimits')}</h2>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>{t('loggedAt')}</th>
              <th>{t('limit')}</th>
              <th></th>
              <th>{t('plan')}</th>
              <th>{t('member')}</th>
            </tr>
          </thead>
          <tbody>
            {#each report.limit_hits ?? [] as h}
              <tr>
                <td>{dateTime(h.at)}</td>
                <td>{@render tag(h.kind === 'provider429' ? 'off' : '', label(kindText, h.kind))}</td>
                <td class="muted">{h.evidence ? label(evidenceText, h.evidence) : ''}</td>
                <td>{@render planName(h.plan)}</td>
                <td>{h.member}</td>
              </tr>
            {:else}
              <tr><td colspan="5" class="muted">—</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>

    {#each report.scenarios ?? [] as s (s.id)}
      <section class="card">
        <h2 class="muted small">{t('scenarios')} · {label(scenarioText, s.id)}</h2>
        {@render fiveHourBlock(s.five_hour)}
        <CapacityChart {report} scenario={s} bucket="five_hour" />
        {@render weeklyBlock(s.weekly)}
        <CapacityChart {report} scenario={s} bucket="weekly" />
        {#if (s.members ?? []).length > 0}
          <div class="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{t('member')}</th>
                  <th class="num">{t('fiveHour')}</th>
                  <th class="num">{t('weekly')}</th>
                </tr>
              </thead>
              <tbody>
                {#each s.members ?? [] as m (m.person_id)}
                  <tr>
                    <td>{m.name}</td>
                    <td class="num">
                      {#if m.five_hour.basis !== 'insufficient'}{m.five_hour.hit_windows} / {m.five_hour.windows} · {percent(m.five_hour.peak_percent)}{:else}—{/if}
                    </td>
                    <td class="num">{#if m.weekly.basis !== 'insufficient'}{percent(m.weekly.peak_percent)}{:else}—{/if}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
        {#if (s.weekly.windows ?? []).length > 0}
          <details>
            <summary class="muted small">{t('formula')}</summary>
            <p class="muted small">{t('formulaCapacity')}</p>
            <div class="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{t('start')}</th>
                  <th class="num">{t('peak')}</th>
                  <th class="num">{t('atLimit')}</th>
                </tr>
              </thead>
              <tbody>
                {#each s.weekly.windows as w (w.start)}
                  <tr>
                    <td>{dateTime(w.start)}</td>
                    <td class="num">{percent(w.peak_percent)}</td>
                    <td class="num">{w.hit_at ? dateTime(w.hit_at) : '—'}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
            </div>
          </details>
        {/if}
      </section>
    {/each}
    <p class="muted small">{t('replayNote')}</p>
  {/if}
{/if}

<style>
  .periods { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; margin-bottom: 8px; }
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
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 8px;
    min-width: 0;
  }
  .stats { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
  .stat-col { display: flex; flex-direction: column; gap: 2px; }
  .stat-num { font-size: 16px; font-weight: 600; color: var(--text); }
  .small { font-size: 11.5px; }
  h2 { margin: 0; font-weight: normal; }
  p { margin: 0; }
  .table-wrap { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 11.5px; }
  th, td { padding: 4px 6px; text-align: left; font-weight: normal; white-space: nowrap; }
  th { color: var(--muted); }
  th.num, td.num { text-align: right; }
  tbody tr:not(:last-child):not(.sub-parent) { border-bottom: 1px solid var(--line); }
  tr.sub td { padding-top: 0; }
  .block-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .figures { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 2px 12px; font-size: 11.5px; }
  .figures .num { text-align: right; }
  .tag {
    font-size: 10px;
    letter-spacing: .02em;
    padding: 1px 5px;
    border-radius: 4px;
    color: var(--accent);
    background: var(--accent-soft);
  }
  .tag.derived, .tag.borderline { color: var(--warn); background: var(--warn-soft); }
  .tag.over { color: var(--danger); background: var(--line); }
  .tag.insufficient, .tag.off { color: var(--muted); background: var(--line); }
  details { display: grid; gap: 6px; min-width: 0; }
  summary { cursor: pointer; }
</style>
