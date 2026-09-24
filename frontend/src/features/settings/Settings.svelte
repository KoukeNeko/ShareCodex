<script lang="ts">
  import { Desktop, errorMessage, type State } from '../../lib/api'

  let { app }: { app: State } = $props()

  let busy = $state(false)
  let error = $state('')
  let confirmingLeave = $state(false)

  async function run(action: () => Promise<void>) {
    busy = true
    error = ''
    try {
      await action()
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = false
    }
  }
</script>

<div class="settings">
  {#if error}<p class="error">{error}</p>{/if}

  <section>
    <h2>伺服器</h2>
    {#if app.paired}
      <dl>
        <dt>成員</dt><dd>{app.person_name}</dd>
        <dt>裝置</dt><dd>{app.device_name}</dd>
        <dt>位址</dt><dd class="url">{app.server_url}</dd>
      </dl>
      {#if confirmingLeave}
        <div class="confirm">
          <p><strong>離開伺服器？</strong><br /><span class="muted">已上傳的用量會保留在伺服器。</span></p>
          <div class="actions">
            <button onclick={() => (confirmingLeave = false)} disabled={busy}>取消</button>
            <button class="danger" onclick={() => run(async () => { await Desktop.Leave(); confirmingLeave = false })} disabled={busy}>離開</button>
          </div>
        </div>
      {:else}
        <button class="danger" onclick={() => (confirmingLeave = true)}>離開伺服器</button>
      {/if}
    {:else}
      <p class="muted">未加入</p>
    {/if}
  </section>

  <section>
    <h2>Claude Code 額度</h2>
    <p class="muted">Claude 的 5 小時與每週額度只能從 statusLine 取得。啟用後，原本的 statusLine 仍照常顯示。</p>
    <div class="row">
      <span>statusLine 擷取</span>
      {#if app.status_line_installed}
        <button onclick={() => run(() => Desktop.RestoreStatusLine())} disabled={busy}>停用</button>
      {:else}
        <button class="primary" onclick={() => run(() => Desktop.InstallStatusLine())} disabled={busy}>啟用</button>
      {/if}
    </div>
  </section>

  <section>
    <h2>一般</h2>
    <label class="row">
      <span>登入時啟動</span>
      <input type="checkbox" checked={app.launch_at_login} disabled={busy}
        onchange={(e) => run(() => Desktop.SetLaunchAtLogin(e.currentTarget.checked))} />
    </label>
    <div class="row">
      <span class="muted">版本 {app.version}</span>
      <button onclick={() => Desktop.Quit()}>結束 ShareCodex</button>
    </div>
  </section>
</div>

<style>
  .settings { display: grid; gap: 10px; }
  section {
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 12px 14px;
    display: grid;
    gap: 8px;
    justify-items: start;
  }
  h2 { margin: 0; font-size: 13px; font-weight: 600; }
  p { margin: 0; }
  dl { margin: 0; display: grid; grid-template-columns: auto 1fr; gap: 3px 12px; width: 100%; }
  dt { color: var(--muted); }
  dd { margin: 0; min-width: 0; }
  .url { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .row { display: flex; justify-content: space-between; align-items: center; width: 100%; }
  .confirm { display: grid; gap: 8px; width: 100%; }
  .actions { display: flex; gap: 6px; justify-content: flex-end; }
</style>
