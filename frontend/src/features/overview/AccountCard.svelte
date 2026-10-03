<script lang="ts">
  import QuotaBar, { type Segment } from '../../components/QuotaBar.svelte'
  import UsageChart from './UsageChart.svelte'
  import type { ActiveUser, BucketOverview } from '../../lib/api'
  import { bucketName, bucketTab, modelName, percent, providerName, resetsIn, tokens } from '../../lib/format'
  import { t } from '../../lib/i18n.svelte'
  import { sortModels } from '../../lib/sort'

  let { id = '', provider, label, planType, buckets, usage, activeUsers, now, local = false, fineChart = false, modelSort = '', onMember, onCapacity, onGrip, pinned = false, onPin, onClose }: {
    id?: string
    provider: string
    label: string
    planType: string
    buckets: BucketOverview[]
    // An account with no quota reading shows its last 5 hours instead.
    usage?: BucketOverview | null
    activeUsers: ActiveUser[]
    now: Date
    local?: boolean
    fineChart?: boolean
    // settings.Settings.ModelSort: how the window's models are ordered.
    modelSort?: string
    // Opens a member's usage; without it, names are plain text.
    onMember?: (personId: string, name: string) => void
    // Opens an account's capacity analysis.
    onCapacity?: (accountId: string, accountLabel: string) => void
    // Starts dragging the card to reorder the list; without it the card
    // has no grip.
    onGrip?: (e: PointerEvent) => void
    // The popup's card pins its account to the screen, showing whether it
    // is; the widget's card closes the widget instead, and its header moves
    // the window.
    pinned?: boolean
    onPin?: () => void
    onClose?: () => void
  } = $props()

  // The viewer's own computer is signed into this account.
  const current = $derived(activeUsers.some((u) => u.is_you))
  const widget = $derived(!!onClose)

  let selected = $state(0)
  const noQuota = $derived(buckets.length === 0)
  const bucket = $derived(noQuota ? usage : buckets[Math.min(selected, buckets.length - 1)])
  const expired = $derived(bucket?.reset || (bucket?.resets_at && new Date(bucket.resets_at) <= now))

  function tone(used: number): 'accent' | 'warn' | 'danger' {
    return used >= 90 ? 'danger' : used >= 70 ? 'warn' : 'accent'
  }

  // A model is keyed with the gateway it came through, so a model reached
  // from another client (gpt-6-sol from Claude Code through OpenCodex) is
  // kept apart from the same model used directly.
  const modelKey = (model: string, gateway = '') => `${gateway}:${model}`

  // Colors follow the model's rank in this window; past the fourth, models
  // share the "other" color so hues are never generated.
  const modelSlots = 4
  const sortedModels = $derived(sortModels(bucket?.models ?? [], modelSort))
  const modelColor = $derived.by(() => {
    const colors = new Map<string, string>()
    sortedModels.forEach((m, i) =>
      colors.set(modelKey(m.model, m.gateway), i < modelSlots ? `var(--model-${i + 1})` : 'var(--model-other)'),
    )
    return colors
  })

  // Third-party models take the same hues in their own order; dashed marks
  // keep them apart from the quota's models.
  const thirdPartyKey = modelKey
  const thirdPartyColor = $derived.by(() => {
    const colors = new Map<string, string>()
    ;(bucket?.third_party_models ?? []).forEach((m, i) =>
      colors.set(thirdPartyKey(m.model, m.gateway), i < modelSlots ? `var(--model-${i + 1})` : 'var(--model-other)'),
    )
    return colors
  })

  // Third-party bars use the Models bars' scale, the quota share the
  // window's own models used per token, so a few million tokens read as
  // small beside a billion. Without official usage there is no scale to
  // borrow, and a bar is its share of the third-party tokens instead.
  // Without a quota, bars are each model's share of the window's tokens.
  function tokenShare(tokensUsed: number): number {
    const all = [...(bucket?.models ?? []), ...(bucket?.third_party_models ?? [])].reduce((sum, m) => sum + m.tokens, 0)
    return all ? (tokensUsed / all) * 100 : 0
  }

  function thirdPartyBar(tokensUsed: number): number {
    if (noQuota) return tokenShare(tokensUsed)
    const models = bucket?.models ?? []
    const officialTokens = models.reduce((sum, m) => sum + m.tokens, 0)
    const officialPercent = models.reduce((sum, m) => sum + m.used_percent, 0)
    if (officialTokens > 0 && officialPercent > 0) return (tokensUsed / officialTokens) * officialPercent
    const thirdPartyTokens = (bucket?.third_party_models ?? []).reduce((sum, m) => sum + m.tokens, 0)
    return thirdPartyTokens ? (tokensUsed / thirdPartyTokens) * 100 : 0
  }

  function segmentsFor(models: { model: string; gateway?: string; used_percent: number }[] | null | undefined): Segment[] {
    return (models ?? []).map((m) => ({
      value: m.used_percent,
      color: modelColor.get(modelKey(m.model, m.gateway)) ?? 'var(--model-other)',
      label: `${m.model} ${percent(m.used_percent)}`,
    }))
  }
</script>

<section class="card" class:current class:widget>
  <header>
    {#if onGrip}
      <button class="grip" title={t('reorder')} aria-label={t('reorder')} onpointerdown={onGrip}>
        <svg width="10" height="14" viewBox="0 0 10 14" fill="currentColor"><circle cx="3" cy="3" r="1.2" /><circle cx="7" cy="3" r="1.2" /><circle cx="3" cy="7" r="1.2" /><circle cx="7" cy="7" r="1.2" /><circle cx="3" cy="11" r="1.2" /><circle cx="7" cy="11" r="1.2" /></svg>
      </button>
    {/if}
    <span class="provider">{providerName(provider)}</span>
    <span class="label">{label}</span>
    {#if planType}
      {#if onCapacity && id}
        <button class="plan" title={t('capacity')} onclick={() => onCapacity(id, label)}>{planType}</button>
      {:else}
        <span class="plan">{planType}</span>
      {/if}
    {/if}
    {#if onClose}
      <button class="corner" title={t('close')} aria-label={t('close')} onclick={onClose}>
        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M2.5 2.5l7 7M9.5 2.5l-7 7" /></svg>
      </button>
    {:else if onPin}
      <button class="corner" class:on={pinned} aria-pressed={pinned} title={pinned ? t('unpin') : t('pin')} aria-label={t('pin')} onclick={onPin}>
        <svg width="13" height="13" viewBox="0 0 16 16" fill={pinned ? 'currentColor' : 'none'} stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"><path d="M10.2 1.8l4 4-2.1 1.1-2.6 2.6.4 3.3-1.3 1.3-2.8-2.8L2.3 14.8l-1.1-1.1 3.5-3.5-2.8-2.8 1.3-1.3 3.3.4 2.6-2.6z" /></svg>
      </button>
    {/if}
  </header>

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

  {#if noQuota}<p class="muted empty">{t('noQuota')}</p>{/if}
  {#if bucket}
    {#if buckets.length > 1}
      <div class="tabs" role="tablist">
        {#each buckets as b, i (b.key)}
          <button role="tab" aria-selected={i === selected} class:active={i === selected} onclick={() => (selected = i)}>
            {bucketTab(b.key, b.window_minutes)}
            <span class="num">{b.reset || (b.resets_at && new Date(b.resets_at) <= now) ? '—' : percent(b.used_percent)}</span>
          </button>
        {/each}
      </div>
    {/if}

    {#if !local && bucket.timeline}
      <UsageChart
        timeline={bucket.timeline}
        models={sortedModels.map((m) => ({ model: m.model, gateway: m.gateway ?? '' }))}
        thirdPartyModels={(bucket.third_party_models ?? []).map((m) => ({ model: m.model, gateway: m.gateway ?? '' }))}
        color={(model, thirdParty, gateway) =>
          (thirdParty ? thirdPartyColor : modelColor).get(modelKey(model, gateway)) ?? 'var(--model-other)'}
        members={bucket.members ?? []}
        {now}
        fine={fineChart}
      />
    {/if}

    {#if !noQuota}
    <div class="total">
      <div class="row">
        <span>{buckets.length === 1 ? bucketName(bucket.key, bucket.window_minutes) : t('account')}</span>
        <span class="num strong">{expired ? '—' : percent(bucket.used_percent)}</span>
      </div>
      {#if !expired}<QuotaBar value={bucket.used_percent} tone={tone(bucket.used_percent)} />{/if}
      <div class="muted small">{expired ? t('reset') : resetsIn(bucket.resets_at, now)}</div>
    </div>
    {/if}

    {#if !local && !expired}
      {#if !noQuota}
      <ul class="members">
        {#each bucket.members ?? [] as m (m.person_id)}
          {@const over = m.used_percent > m.allotted_percent + 0.5}
          <li>
            <div class="row">
              <span class="name">
                {#if onMember}<button class="member" onclick={() => onMember(m.person_id, m.name)}>{m.name}</button>{:else}{m.name}{/if}
                {#if m.is_you}<span class="you">{t('you')}</span>{/if}
              </span>
              <span class="num">
                {#if over}<span class="tag">{t('overAllotment')}</span>{/if}
                {t('estimated', { percent: percent(m.used_percent) })}<span class="muted allot">{t('allotted', { percent: percent(m.allotted_percent) })}</span>
              </span>
            </div>
            <QuotaBar value={m.used_percent} marker={m.allotted_percent} segments={segmentsFor(m.models)} thin />
            {#if (m.devices ?? []).length > 0}
              <ul class="devices">
                {#each m.devices ?? [] as d (d.name)}
                  <li>
                    <div class="row muted small">
                      <span class="device-name">{d.name}</span>
                      <span class="num">{percent(d.used_percent)}</span>
                    </div>
                    <!-- One device's bar would repeat the member's. -->
                    {#if (m.devices ?? []).length > 1}<QuotaBar value={d.used_percent} tone="muted" thin />{/if}
                  </li>
                {/each}
              </ul>
            {/if}
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
      {/if}

      {#if sortedModels.length > 0}
        <ul class="models">
          <li class="muted small">{t('models')}</li>
          {#each sortedModels as model (modelKey(model.model, model.gateway))}
            {@const shown = modelName(model.model, model.gateway)}
            <li>
              <div class="row">
                <span class="model" title={shown.title}><i class="swatch" style:background={modelColor.get(modelKey(model.model, model.gateway))}></i><span class="model-name">{shown.name}</span>{#if shown.via}<span class="via">{shown.via}</span>{/if}</span>
                <span class="num usage"><span class="muted">{t('tokens', { count: tokens(model.tokens) })}</span>{#if !noQuota}{t('estimated', { percent: percent(model.used_percent) })}{/if}</span>
              </div>
              {#if noQuota}
                <QuotaBar value={tokenShare(model.tokens)} segments={[{ value: tokenShare(model.tokens), color: modelColor.get(modelKey(model.model, model.gateway)) ?? 'var(--model-other)', label: model.model }]} thin />
              {:else}
                <QuotaBar value={model.used_percent} segments={segmentsFor([model])} thin />
              {/if}
            </li>
          {/each}
        </ul>
      {/if}

      {#if (bucket.third_party_models ?? []).length > 0}
        <ul class="models third-list">
          <li class="muted small">{t('thirdPartyModels')}</li>
          {#each bucket.third_party_models ?? [] as model (thirdPartyKey(model.model, model.gateway))}
            {@const shown = modelName(model.model, model.gateway)}
            {@const color = thirdPartyColor.get(thirdPartyKey(model.model, model.gateway)) ?? 'var(--model-other)'}
            <li>
              <div class="row">
                <span class="model" title={shown.title}><i class="swatch third" style:color={color}></i><span class="model-name">{shown.name}</span>{#if shown.via}<span class="via">{shown.via}</span>{/if}</span>
                <span class="num usage third-usage">{t('tokens', { count: tokens(model.tokens) })}</span>
              </div>
              <QuotaBar
                value={thirdPartyBar(model.tokens)}
                segments={[{ value: thirdPartyBar(model.tokens), color, label: model.model }]}
                thin
              />
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
    grid-template-columns: minmax(0, 1fr);
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
  .grip { flex: none; align-self: center; border: none; background: none; padding: 0 2px; margin-left: -4px; color: var(--muted); cursor: grab; touch-action: none; display: flex; }
  .grip:hover { color: var(--text); }
  .corner { flex: none; align-self: center; display: flex; padding: 3px; border: none; background: none; color: var(--muted); }
  .corner:hover:not(:disabled) { color: var(--text); }
  .corner.on { color: var(--accent); }
  /* A widget is its window: no frame of its own, and its header moves it. */
  .card.widget { border: none; border-radius: 0; background: none; }
  .widget header { --wails-draggable: drag; cursor: default; }
  .widget header button { --wails-draggable: no-drag; }
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
    white-space: nowrap;
  }
  .tabs button.active { background: var(--surface); color: var(--text); box-shadow: 0 1px 2px rgb(0 0 0 / .08); }
  .total { display: grid; gap: 5px; }
  .row { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
  .strong { font-weight: 600; font-size: 15px; }
  .small { font-size: 11.5px; }
  .empty { margin: 0; }
  /* Every list takes the card's width and no more, so a long name is cut
     off instead of pushing its row past the card's padding. The space
     under a divider matches the card's gap above it. */
  .members, .models, .devices, .members li, .models li, .devices li { grid-template-columns: minmax(0, 1fr); }
  .members { list-style: none; margin: 0; padding: 10px 0 0; border-top: 1px solid var(--line); display: grid; gap: 9px; }
  .members li { display: grid; gap: 4px; }
  .allot { margin-left: 8px; }
  .devices { list-style: none; margin: 2px 0 0; padding: 0; display: grid; gap: 4px; }
  .devices li { display: grid; gap: 3px; }
  .device-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  .models { list-style: none; margin: 0; padding: 10px 0 0; border-top: 1px solid var(--line); display: grid; gap: 9px; }
  .models li { display: grid; gap: 4px; }
  .models .muted { margin-right: 8px; }
  .model { min-width: 0; display: flex; align-items: center; gap: 6px; }
  .model-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  /* The numbers keep their line; a long model name is cut instead. */
  .usage { flex: none; white-space: nowrap; }
  .swatch { width: 8px; height: 8px; border-radius: 2px; flex: none; }
  .swatch.third { border-radius: 0; height: 2px; background: repeating-linear-gradient(90deg, currentColor 0 3px, transparent 3px 5px); }
  .third-usage { color: var(--muted); }
  .via {
    flex: none;
    font-size: 10px;
    letter-spacing: .04em;
    color: var(--muted);
    border: 1px solid var(--line);
    border-radius: 4px;
    padding: 0 4px;
  }
  /* The dashed swatch has no text baseline to line the row up by. */
  .third-list .row { align-items: center; }
  .name { display: flex; align-items: center; gap: 6px; }
  .member { border: none; background: none; padding: 0; color: inherit; }
  .member:hover:not(:disabled) { text-decoration: underline; }
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
