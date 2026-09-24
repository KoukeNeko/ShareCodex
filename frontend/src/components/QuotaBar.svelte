<script lang="ts">
  export type Segment = { value: number; color: string; label: string }

  // A horizontal meter. `marker` draws a tick at a member's allotment;
  // `segments` stacks several fills (one per model) instead of one.
  let { value, marker = null, tone = 'accent', thin = false, segments = null }: {
    value: number
    marker?: number | null
    tone?: 'accent' | 'warn' | 'danger' | 'muted'
    thin?: boolean
    segments?: Segment[] | null
  } = $props()

  const clamp = (v: number) => Math.max(0, Math.min(100, v))
</script>

<div class="track" class:thin role="meter" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(value)}>
  {#if segments}
    <div class="stack">
      {#each segments as s (s.label)}
        <div class="seg" style:width="{clamp(s.value)}%" style:background={s.color} title={s.label}></div>
      {/each}
    </div>
  {:else}
    <div class="fill {tone}" style:width="{clamp(value)}%"></div>
  {/if}
  {#if marker !== null}
    <div class="marker" style:left="{clamp(marker)}%"></div>
  {/if}
</div>

<style>
  .track {
    position: relative;
    height: 8px;
    border-radius: 4px;
    background: var(--track);
    overflow: visible;
  }
  .track.thin { height: 5px; }
  .fill {
    height: 100%;
    border-radius: inherit;
    transition: width .3s ease;
  }
  .accent { background: var(--accent); }
  .warn { background: var(--warn); }
  .danger { background: var(--danger); }
  .muted { background: var(--muted); }
  .stack {
    display: flex;
    height: 100%;
    border-radius: inherit;
    overflow: hidden;
  }
  .seg {
    height: 100%;
    flex: none;
    transition: width .3s ease;
  }
  /* A 2px surface gap separates adjacent segments. */
  .seg + .seg { box-shadow: inset 2px 0 0 var(--surface); }
  .marker {
    position: absolute;
    top: -3px;
    bottom: -3px;
    width: 2px;
    margin-left: -1px;
    background: var(--text);
    opacity: .55;
    border-radius: 1px;
  }
</style>
