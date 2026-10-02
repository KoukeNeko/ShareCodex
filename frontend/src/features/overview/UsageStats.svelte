<script lang="ts">
  import type { UsageTotals } from '../../lib/api'
  import { inputTokens, percent, tokens, totalTokens, usd } from '../../lib/format'
  import { t } from '../../lib/i18n.svelte'

  // Each column is the totals of one span; onDetails, when given, opens a
  // member's view from the empty corner.
  let { columns, onDetails }: { columns: { label: string; totals: UsageTotals }[]; onDetails?: () => void } = $props()

  function cached(u: UsageTotals): string {
    const input = inputTokens(u)
    return input > 0 ? t('cachedShare', { percent: percent((u.cached_input / input) * 100) }) : ''
  }
</script>

<section class="card">
  <table>
    <thead>
      <tr>
        <th>{#if onDetails}<button class="details" onclick={onDetails}>{t('details')}</button>{/if}</th>
        {#each columns as c (c.label)}<th class="muted">{c.label}</th>{/each}
      </tr>
    </thead>
    <tbody>
      <tr class="total">
        <th class="muted">{t('totalTokens')}</th>
        {#each columns as c (c.label)}<td class="num">{tokens(totalTokens(c.totals))}</td>{/each}
      </tr>
      <tr>
        <th class="muted">{t('input')}</th>
        {#each columns as c (c.label)}<td class="num">{tokens(inputTokens(c.totals))}<span class="sub muted">{cached(c.totals)}</span></td>{/each}
      </tr>
      <tr>
        <th class="muted">{t('output')}</th>
        {#each columns as c (c.label)}<td class="num">{tokens(c.totals.output)}</td>{/each}
      </tr>
      <tr>
        <th class="muted">{t('estCost')}</th>
        {#each columns as c (c.label)}<td class="num">{usd(c.totals.cost_usd)}</td>{/each}
      </tr>
      <tr>
        <th class="muted">{t('requests')}</th>
        {#each columns as c (c.label)}<td class="num">{tokens(c.totals.requests)}</td>{/each}
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
  thead th:first-child { text-align: left; }
  tbody th { width: 38%; }
  .total td { font-size: 16px; font-weight: 600; }
  .sub { display: block; font-size: 11px; }
  .details { padding: 1px 8px; font-size: 11.5px; color: var(--muted); }
</style>
