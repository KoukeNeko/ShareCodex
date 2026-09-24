import { mount } from 'svelte'
import { System } from '@wailsio/runtime'
import App from './App.svelte'

// macOS draws the popup on Liquid Glass; the stylesheet makes the page
// translucent there so the material shows through.
if (System.IsMac()) document.documentElement.classList.add('macos')

mount(App, { target: document.getElementById('app')! })
