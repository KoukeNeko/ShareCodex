<script lang="ts">
  // A horizontal meter. `marker` draws a tick at a member's allotment.
  let { value, marker = null, tone = 'accent', thin = false }: {
    value: number
    marker?: number | null
    tone?: 'accent' | 'warn' | 'danger' | 'muted'
    thin?: boolean
  } = $props()

  const clamp = (v: number) => Math.max(0, Math.min(100, v))
</script>

<div class="track" class:thin role="meter" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(value)}>
  <div class="fill {tone}" style:width="{clamp(value)}%"></div>
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
