import { toPng } from 'html-to-image'

// Masked or not, an address names a person: do***@alum.ccu.edu.tw still
// shows the domain and the start of the name.
const emailPattern = /[^\s@<>()]+@[^\s@<>()]+\.[^\s@<>()]+/g

/**
 * Renders the popup to a PNG, whole, as if it were tall enough for all of its
 * content, with every email address replaced by a numbered placeholder.
 * Returns the PNG base64-encoded.
 */
export async function renderShareImage(main: HTMLElement, placeholder: (n: number) => string): Promise<string> {
  const clone = main.cloneNode(true) as HTMLElement
  clone.querySelector('header .tools')?.remove()
  clone.querySelectorAll('.tip, .grip').forEach((el) => el.remove())
  clone.querySelectorAll('[title]').forEach((el) => el.removeAttribute('title'))
  anonymize(clone, placeholder)

  // Laid out off screen at the popup's width and its full height.
  const frame = document.createElement('div')
  frame.className = 'share-capture'
  frame.style.width = `${main.offsetWidth}px`
  clone.style.height = 'auto'
  const content = clone.querySelector<HTMLElement>('.content')
  if (content) {
    content.style.overflow = 'visible'
    content.style.flex = 'none'
  }
  frame.appendChild(clone)
  document.body.appendChild(frame)
  try {
    const url = await toPng(frame, {
      pixelRatio: 2,
      // The popup uses system fonts only; fetching stylesheets to embed web
      // fonts would only add seconds.
      skipFonts: true,
      backgroundColor: getComputedStyle(frame).backgroundColor,
      // The frame is only off screen to lay it out; drawn in place.
      style: { position: 'static', left: '0', top: '0' },
    })
    return url.slice(url.indexOf(',') + 1)
  } finally {
    frame.remove()
  }
}

function anonymize(root: HTMLElement, placeholder: (n: number) => string) {
  const numbers = new Map<string, number>()
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const text = node.nodeValue ?? ''
    if (!text.includes('@')) continue
    node.nodeValue = text.replace(emailPattern, (email) => {
      if (!numbers.has(email)) numbers.set(email, numbers.size + 1)
      return placeholder(numbers.get(email)!)
    })
  }
}
