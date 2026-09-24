<script lang="ts">
  import { Desktop, errorMessage } from '../../lib/api'
  import { t } from '../../lib/i18n.svelte'

  let link = $state('')
  let busy = $state(false)
  let error = $state('')

  async function join(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    error = ''
    try {
      await Desktop.Join(link.trim())
      link = ''
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = false
    }
  }
</script>

<form class="card" onsubmit={join}>
  <h2>{t('joinServer')}</h2>
  <div class="field">
    <input type="text" bind:value={link} placeholder={t('pasteLink')} spellcheck="false" autocomplete="off" />
    <button class="primary" type="submit" disabled={busy || link.trim() === ''}>{t('join')}</button>
  </div>
  {#if error}<p class="error">{error}</p>{/if}
</form>

<style>
  .card {
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 12px 14px;
    display: grid;
    gap: 8px;
  }
  h2 { margin: 0; font-size: 13px; font-weight: 600; }
  .field { display: flex; gap: 6px; }
  .error { margin: 0; }
</style>
