<script lang="ts">
  import QuotaBar, { type Segment } from '../../components/QuotaBar.svelte'
  import { Desktop, errorMessage, type ActiveUser, type BucketOverview } from '../../lib/api'
  import { bucketName, percent, providerName, resetsIn, tokens } from '../../lib/format'
  import { t } from '../../lib/i18n.svelte'

  let { id = '', provider, label, planType, buckets, activeUsers, now, local = false }: {
    id?: string
    provider: string
    label: string
    planType: string
    buckets: BucketOverview[]
    activeUsers: ActiveUser[]
    now: Date
    local?: boolean
  } = $props()

  // The viewer's own computer is signed into this account.
  const current = $derived(activeUsers.some((u) => u.is_you))

  let confirmingLeave = $state(false)
  let leaving = $state(false)
  let leaveError = $state('')

  async function leave() {
    leaving = true
    leaveError = ''
    try {
      await Desktop.LeaveAccount(id)
    } catch (err) {
      leaveError = errorMessage(err)
    } finally {
      leaving = false
    }
  }

  let selected = $state(0)
  const bucket = $derived(buckets[Math.min(selected, buckets.length - 1)])
  const expired = $derived(bucket?.reset || (bucket?.resets_at && new Date(bucket.resets_at) <= now))

  function tone(used: number): 'accent' | 'warn' | 'danger' {
    return used >= 90 ? 'danger' : used >= 70 ? 'warn' : 'accent'
  }

  // Colors follow the model's rank in this window; past the fourth, models
  // share the "other" color so hues are never generated.
  const modelSlots = 4
  const modelColor = $derived.by(() => {
    const colors = new Map<string, string>()
    ;(bucket?.models ?? []).forEach((m, i) =>
      colors.set(m.model, i < modelSlots ? `var(--model-${i + 1})` : 'var(--model-other)'),
    )
    return colors
  })

  function segmentsFor(models: { model: string; used_percent: number }[] | null | undefined): Segment[] {
    return (models ?? []).map((m) => ({
      value: m.used_percent,
      color: modelColor.get(m.model) ?? 'var(--model-other)',
      label: `${m.model} ${percent(m.used_percent)}`,
    }))
  }
</script>

<section class="card" class:current>
  <header>
    <span class="provider">{providerName(provider)}</span>
    <span class="label">{label}</span>
    {#if planType}<span class="plan">{planType}</span>{/if}
    {#if !local && !confirmingLeave}
      <button class="leave" onclick={() => (confirmingLeave = true)}>{t('leave')}</button>
    {/if}
  </header>

  {#if confirmingLeave}
    <div class="confirm">
      <p><strong>{t('leaveAccountConfirm')}</strong><br /><span class="muted">{t('leaveAccountBody')}</span></p>
      {#if leaveError}<p class="error">{leaveError}</p>{/if}
      <div class="actions">
        <button onclick={() => (confirmingLeave = false)} disabled={leaving}>{t('cancel')}</button>
        <button class="danger" onclick={leave} disabled={leaving}>{t('leave')}</button>
      </div>
    </div>
  {/if}

  {#if activeUsers.length > 0}
    <div class="active">
      <span class="muted">{t('inUse')}</span>
      {#each activeUsers as u (u.person_id)}
        {#if u.is_you}
          <span class="you" title={(u.devices ?? []).join(', ')}>{t('you')}</span>
        {:else}
          <span class="user" title={(u.devices ?? []).join(', ')}>{u.name}</span>
        {/if}
      {/each}
    </div>
  {/if}

  {#if buckets.length === 0}
    <p class="muted empty">{t('noQuota')}</p>
  {:else}
    {#if buckets.length > 1}
      <div class="tabs" role="tablist">
        {#each buckets as b, i (b.key)}
          <button role="tab" aria-selected={i === selected} class:active={i === selected} onclick={() => (selected = i)}>
            {bucketName(b.key, b.window_minutes)}
            <span class="num">{b.reset || (b.resets_at && new Date(b.resets_at) <= now) ? '—' : percent(b.used_percent)}</span>
          </button>
        {/each}
      </div>
    {/if}

    <div class="total">
      <div class="row">
        <span>{buckets.length === 1 ? bucketName(bucket.key, bucket.window_minutes) : t('account')}</span>
        <span class="num strong">{expired ? '—' : percent(bucket.used_percent)}</span>
      </div>
      {#if !expired}<QuotaBar value={bucket.used_percent} tone={tone(bucket.used_percent)} />{/if}
      <div class="muted small">{expired ? t('reset') : resetsIn(bucket.resets_at, now)}</div>
    </div>

    {#if !local && !expired}
      <ul class="members">
        {#each bucket.members ?? [] as m (m.person_id)}
          {@const over = m.used_percent > m.allotted_percent + 0.5}
          <li>
            <div class="row">
              <span class="name">{m.name}{#if m.is_you}<span class="you">{t('you')}</span>{/if}</span>
              <span class="num">
                {#if over}<span class="tag">{t('overAllotment')}</span>{/if}
                {t('estimated', { percent: percent(m.used_percent) })}<span class="muted allot">{t('allotted', { percent: percent(m.allotted_percent) })}</span>
              </span>
            </div>
            <QuotaBar value={m.used_percent} marker={m.allotted_percent} segments={segmentsFor(m.models)} thin />
          </li>
        {/each}
        {#if bucket.unattributed_percent > 0}
          <li>
            <div class="row">
              <span class="name muted">{t('unattributed')}</span>
              <span class="num muted">{percent(bucket.unattributed_percent)}</span>
            </div>
            <QuotaBar value={bucket.unattributed_percent} tone="muted" thin />
          </li>
        {/if}
      </ul>

      {#if (bucket.models ?? []).length > 0}
        <ul class="models">
          <li class="muted small">{t('models')}</li>
          {#each bucket.models ?? [] as model (model.model)}
            <li>
              <div class="row">
                <span class="model" title={model.model}><i class="swatch" style:background={modelColor.get(model.model)}></i><span class="model-name">{model.model}</span></span>
                <span class="num usage"><span class="muted">{t('tokens', { count: tokens(model.tokens) })}</span>{t('estimated', { percent: percent(model.used_percent) })}</span>
              </div>
              <QuotaBar value={model.used_percent} segments={segmentsFor([model])} thin />
            </li>
          {/each}
        </ul>
      {/if}
    {/if}
  {/if}
</section>

<style>
  .card {
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 14px;
    display: grid;
    gap: 10px;
  }
  .card.current { border-color: var(--accent); }
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
  .leave { flex: none; padding: 1px 6px; font-size: 11px; color: var(--muted); }
  .confirm { display: grid; gap: 8px; }
  .confirm p { margin: 0; }
  .actions { display: flex; gap: 6px; justify-content: flex-end; }
  .active { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 6px; font-size: 11.5px; }
  .active .muted { margin-right: 2px; }
  .user { background: var(--track); border-radius: 4px; padding: 0 5px; }
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
  .models { list-style: none; margin: 0; padding: 8px 0 0; border-top: 1px solid var(--line); display: grid; gap: 9px; }
  .models li { display: grid; gap: 4px; }
  .models .muted { margin-right: 8px; }
  .model { min-width: 0; display: flex; align-items: center; gap: 6px; }
  .model-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  /* The numbers keep their line; a long model name is cut instead. */
  .usage { flex: none; white-space: nowrap; }
  .swatch { width: 8px; height: 8px; border-radius: 2px; flex: none; }
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
