<script lang="ts">
  import UsageStats from './UsageStats.svelte'
  import { Desktop, errorMessage, type MemberUsage } from '../../lib/api'
  import { modelName, providerName, tokens, totalTokens, usd } from '../../lib/format'
  import { t } from '../../lib/i18n.svelte'

  let { personId }: { personId: string } = $props()

  // The spans the server offers a member's usage over.
  const periods = ['24h', '7d', '30d']
  const periodLabel = (id: string) => (id === '24h' ? t('hours', { count: 24 }) : t('days', { count: parseInt(id) }))

  let period = $state('30d')
  let usage = $state<MemberUsage | null>(null)
  let error = $state('')

  // Fetches again when the span changes; the last answer stays up meanwhile,
  // and an answer to an earlier span is dropped.
  $effect(() => {
    const id = personId
    const span = period
    let stale = false
    Desktop.MemberUsage(id, span)
      .then((u) => {
        if (stale) return
        usage = u
        error = ''
      })
      .catch((err) => {
        if (!stale) error = errorMessage(err)
      })
    return () => (stale = true)
  })

  type Row = { key: string; name: string; via?: string; title?: string; tokens: number; cost?: number }

  const accounts = $derived<Row[]>(
    (usage?.accounts ?? []).map((a) => ({
      key: a.id,
      name: `${providerName(a.provider)} ${a.label}`,
      tokens: totalTokens(a.totals),
      cost: a.totals.cost_usd,
    })),
  )
  const models = $derived<Row[]>(
    (usage?.models ?? []).map((m) => ({ key: `${m.gateway}:${m.model}`, ...modelName(m.model, m.gateway), tokens: totalTokens(m.totals), cost: m.totals.cost_usd })),
  )
  // Third-party models never count against a quota, so they carry no price.
  const thirdParty = $derived<Row[]>(
    (usage?.third_party_models ?? []).map((m) => ({ key: `${m.gateway}:${m.model}`, ...modelName(m.model, m.gateway), tokens: totalTokens(m.totals) })),
  )
  const devices = $derived<Row[]>(
    (usage?.devices ?? []).map((d) => ({ key: d.name, name: d.name, tokens: totalTokens(d.totals), cost: d.totals.cost_usd })),
  )
  const empty = $derived(!!usage && usage.total.requests === 0 && thirdParty.length === 0)
</script>

{#snippet lines(title: string, rows: Row[])}
  {#if rows.length > 0}
    <section class="card">
      <h2 class="muted small">{title}</h2>
      <ul>
        {#each rows as r (r.key)}
          <li>
            <span class="label" title={r.title ?? r.name}><span class="text">{r.name}</span>{#if r.via}<span class="via">{r.via}</span>{/if}</span>
            <span class="num usage">{tokens(r.tokens)}{#if r.cost !== undefined}<span class="muted cost">{usd(r.cost)}</span>{/if}</span>
          </li>
        {/each}
      </ul>
    </section>
  {/if}
{/snippet}

<div class="periods">
  {#each periods as p (p)}
    <button class:active={period === p} onclick={() => (period = p)}>{periodLabel(p)}</button>
  {/each}
</div>

{#if error}
  <p class="error">{error}</p>
{:else if !usage}
  <p class="muted">{t('loading')}</p>
{:else if empty}
  <p class="muted">{t('noUsage')}</p>
{:else}
  <UsageStats columns={[{ label: periodLabel(usage.period), totals: usage.total }]} />
  {@render lines(t('accounts'), accounts)}
  {@render lines(t('models'), models)}
  {@render lines(t('thirdPartyModels'), thirdParty)}
  {@render lines(t('devices'), devices)}
{/if}

<style>
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
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 8px;
  }
  h2 { margin: 0; font-weight: normal; }
  .small { font-size: 11.5px; }
  ul { list-style: none; margin: 0; padding: 0; display: grid; grid-template-columns: minmax(0, 1fr); gap: 6px; }
  li { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
  .label { min-width: 0; display: flex; align-items: center; gap: 6px; }
  .text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  /* The numbers keep their line; a long name is cut instead. */
  .usage { flex: none; white-space: nowrap; }
  .cost { margin-left: 8px; }
  .via {
    flex: none;
    font-size: 10px;
    letter-spacing: .04em;
    color: var(--muted);
    border: 1px solid var(--line);
    border-radius: 4px;
    padding: 0 4px;
  }
</style>
