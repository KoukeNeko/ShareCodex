import { mount } from 'svelte'
import { System } from '@wailsio/runtime'
import App from './App.svelte'

// macOS draws the popup on Liquid Glass; the stylesheet makes the page
// translucent there so the material shows through.
if (System.IsMac()) document.documentElement.classList.add('macos')
// Windows gets WinUI 3 controls; the backend opts into Acrylic only where DWM
// supports it, so the page stays opaque on older builds. System.IsWindows()
// is still false this early on WebView2, so read the user agent instead.
if (navigator.userAgent.includes('Windows')) document.documentElement.classList.add('windows')
if (new URLSearchParams(location.search).get('backdrop') === 'acrylic') {
  document.documentElement.classList.add('acrylic')
}

mount(App, { target: document.getElementById('app')! })
