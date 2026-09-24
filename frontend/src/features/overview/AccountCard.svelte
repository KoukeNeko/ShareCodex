<script lang="ts">
  import QuotaBar from '../../components/QuotaBar.svelte'
  import type { BucketOverview } from '../../lib/api'
  import { bucketName, percent, providerName, resetsIn } from '../../lib/format'

  let { provider, label, planType, buckets, now, local = false }: {
    provider: string
    label: string
    planType: string
    buckets: BucketOverview[]
    now: Date
    local?: boolean
  } = $props()

  let selected = $state(0)
  const bucket = $derived(buckets[Math.min(selected, buckets.length - 1)])

  function tone(used: number): 'accent' | 'warn' | 'danger' {
    return used >= 90 ? 'danger' : used >= 70 ? 'warn' : 'accent'
  }
</script>

<section class="card">
  <header>
    <span class="provider">{providerName(provider)}</span>
    <span class="label">{label}</span>
    {#if planType}<span class="plan">{planType}</span>{/if}
  </header>

  {#if buckets.length === 0}
    <p class="muted empty">尚無額度資料</p>
  {:else}
    {#if buckets.length > 1}
      <div class="tabs" role="tablist">
        {#each buckets as b, i (b.key)}
          <button role="tab" aria-selected={i === selected} class:active={i === selected} onclick={() => (selected = i)}>
            {bucketName(b.key, b.window_minutes)}
            <span class="num">{percent(b.used_percent)}</span>
          </button>
        {/each}
      </div>
    {/if}

    <div class="total">
      <div class="row">
        <span>{buckets.length === 1 ? bucketName(bucket.key, bucket.window_minutes) : '帳號'}</span>
        <span class="num strong">{percent(bucket.used_percent)}</span>
      </div>
      <QuotaBar value={bucket.used_percent} tone={tone(bucket.used_percent)} />
      <div class="muted small">{bucket.reset ? '已重置' : resetsIn(bucket.resets_at, now)}</div>
    </div>

    {#if !local}
      <ul class="members">
        {#each bucket.members ?? [] as m (m.person_id)}
          {@const over = m.used_percent > m.allotted_percent + 0.5}
          <li>
            <div class="row">
              <span class="name">{m.name}{#if m.is_you}<span class="you">你</span>{/if}</span>
              <span class="num">
                {#if over}<span class="tag">超出分配</span>{/if}
                估計 {percent(m.used_percent)}<span class="muted allot">分配 {percent(m.allotted_percent)}</span>
              </span>
            </div>
            <QuotaBar value={m.used_percent} marker={m.allotted_percent} tone={over ? 'warn' : 'accent'} thin />
          </li>
        {/each}
        {#if bucket.unattributed_percent > 0}
          <li>
            <div class="row">
              <span class="name muted">未歸屬</span>
              <span class="num muted">{percent(bucket.unattributed_percent)}</span>
            </div>
            <QuotaBar value={bucket.unattributed_percent} tone="muted" thin />
          </li>
        {/if}
      </ul>
    {/if}
  {/if}
</section>

<style>
  .card {
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 12px 14px;
    display: grid;
    gap: 10px;
  }
  header { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  .provider { font-weight: 600; }
  .label { color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
  .plan {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: .04em;
    color: var(--accent);
    background: var(--accent-soft);
    border-radius: 4px;
    padding: 1px 6px;
  }
  .tabs { display: flex; gap: 4px; background: var(--track); border-radius: 8px; padding: 2px; }
  .tabs button {
    flex: 1;
    border: none;
    background: none;
    border-radius: 6px;
    padding: 3px 8px;
    color: var(--muted);
    display: flex;
    justify-content: center;
    gap: 6px;
  }
  .tabs button.active { background: var(--surface); color: var(--text); box-shadow: 0 1px 2px rgb(0 0 0 / .08); }
  .total { display: grid; gap: 5px; }
  .row { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
  .strong { font-weight: 600; font-size: 15px; }
  .small { font-size: 11.5px; }
  .empty { margin: 0; }
  .members { list-style: none; margin: 0; padding: 8px 0 0; border-top: 1px solid var(--line); display: grid; gap: 9px; }
  .members li { display: grid; gap: 4px; }
  .allot { margin-left: 8px; }
  .name { display: flex; align-items: center; gap: 6px; }
  .you {
    font-size: 10.5px;
    color: var(--accent);
    border: 1px solid var(--accent);
    border-radius: 4px;
    padding: 0 4px;
  }
  .tag {
    font-size: 10.5px;
    color: var(--warn);
    background: var(--warn-soft);
    border-radius: 4px;
    padding: 1px 5px;
    margin-right: 6px;
  }
</style>
