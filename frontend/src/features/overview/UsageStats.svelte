<script lang="ts">
  import type { PersonalUsage, UsageTotals } from '../../lib/api'
  import { percent, tokens, usd } from '../../lib/format'
  import { t } from '../../lib/i18n.svelte'

  let { usage }: { usage: PersonalUsage } = $props()

  const periods = $derived([usage.today, usage.last_30_days])

  function input(u: UsageTotals): number {
    return u.input + u.cached_input + u.cache_write
  }

  function cached(u: UsageTotals): string {
    return input(u) > 0 ? t('cachedShare', { percent: percent((u.cached_input / input(u)) * 100) }) : ''
  }
</script>

<section class="card">
  <table>
    <thead>
      <tr><th></th><th class="muted">{t('today')}</th><th class="muted">{t('last30Days')}</th></tr>
    </thead>
    <tbody>
      <tr class="total">
        <th class="muted">{t('totalTokens')}</th>
        {#each periods as u, i (i)}<td class="num">{tokens(input(u) + u.output)}</td>{/each}
      </tr>
      <tr>
        <th class="muted">{t('input')}</th>
        {#each periods as u, i (i)}<td class="num">{tokens(input(u))}<span class="sub muted">{cached(u)}</span></td>{/each}
      </tr>
      <tr>
        <th class="muted">{t('output')}</th>
        {#each periods as u, i (i)}<td class="num">{tokens(u.output)}</td>{/each}
      </tr>
      <tr>
        <th class="muted">{t('estCost')}</th>
        {#each periods as u, i (i)}<td class="num">{usd(u.cost_usd)}</td>{/each}
      </tr>
      <tr>
        <th class="muted">{t('requests')}</th>
        {#each periods as u, i (i)}<td class="num">{tokens(u.requests)}</td>{/each}
      </tr>
    </tbody>
  </table>
</section>

<style>
  .card {
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 12px 14px;
  }
  table { width: 100%; border-collapse: collapse; table-layout: fixed; }
  th, td { padding: 3px 0; font-weight: normal; vertical-align: top; }
  th { text-align: left; white-space: nowrap; }
  td, thead th { text-align: right; white-space: nowrap; }
  thead th { padding-bottom: 6px; font-size: 11.5px; }
  tbody th { width: 38%; }
  .total td { font-size: 16px; font-weight: 600; }
  .sub { display: block; font-size: 11px; }
</style>
