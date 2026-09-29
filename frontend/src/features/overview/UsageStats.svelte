<script lang="ts">
  import type { PersonalUsage, UsageTotals } from '../../lib/api'
  import { percent, tokens, usd } from '../../lib/format'
  import { t } from '../../lib/i18n.svelte'

  let { usage }: { usage: PersonalUsage } = $props()

  const periods = $derived([
    { label: t('today'), totals: usage.today },
    { label: t('last30Days'), totals: usage.last_30_days },
  ])

  function input(u: UsageTotals): number {
    return u.input + u.cached_input + u.cache_write
  }
</script>

<section class="card">
  {#each periods as p (p.label)}
    <dl>
      <dt class="period muted">{p.label}</dt>
      <dd></dd>
      <dt class="muted">{t('totalTokens')}</dt>
      <dd class="num total">{tokens(input(p.totals) + p.totals.output)}</dd>
      <dt class="muted">{t('input')}</dt>
      <dd class="num">{tokens(input(p.totals))}</dd>
      {#if input(p.totals) > 0}
        <dt></dt>
        <dd class="num muted small">{t('cachedShare', { percent: percent((p.totals.cached_input / input(p.totals)) * 100) })}</dd>
      {/if}
      <dt class="muted">{t('output')}</dt>
      <dd class="num">{tokens(p.totals.output)}</dd>
      <dt class="muted">{t('estCost')}</dt>
      <dd class="num">{usd(p.totals.cost_usd)}</dd>
      <dt class="muted">{t('requests')}</dt>
      <dd class="num">{tokens(p.totals.requests)}</dd>
    </dl>
  {/each}
</section>

<style>
  .card {
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 14px;
    display: grid;
    grid-template-columns: 1fr 1fr;
  }
  dl {
    margin: 0;
    display: grid;
    grid-template-columns: auto 1fr;
    align-content: start;
    gap: 5px 8px;
  }
  dl + dl { border-left: 1px solid var(--line); padding-left: 14px; margin-left: 14px; }
  dt { white-space: nowrap; }
  dd { margin: 0; text-align: right; }
  .period { grid-column: 1 / -1; margin-bottom: 2px; }
  .period + dd { display: none; }
  .total { font-size: 17px; font-weight: 600; }
  .small { font-size: 11.5px; }
</style>
