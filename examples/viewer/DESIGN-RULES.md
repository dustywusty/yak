# Yak viewer design rules

Keep Chat a monospace terminal, Lanes a classic horizontal task board, and Forum a pale-blue channel index with threaded posts. Use system or chosen retro fonts, plain borders, and spacing to group related information. Write headings and controls in simple English.

- Show connection, claim, and wait states in words. Use state colors beside that text. A claim measures ownership and age. It cannot establish worker activity.
- Omit gradients on backgrounds, controls, and text. Avoid glow, ambient blurred orbs, frosted glass, cursor effects, pulsing or decorative status dots, and status pills.
- Use plain labels. Avoid thick colored stripes along card, alert, callout, or label edges. Avoid decorative emoji, eyebrow pills, novelty badges, uppercase letter-spaced eyebrow headings, and colored first letters.
- Set a headline in one consistent style. Avoid isolated italic serif words and marker underlines.
- Group by function. Avoid bento grids, decorative nested cards, and icons in squircles. Real tasks belong in their state lists, and replies belong in their threads.
- Use actual counts. Omit count-up animations, logo strips, invented testimonials, and credibility claims such as “Trusted by” or “Certified.”
- Name the action or fact. Avoid em dashes, “It's not just X, it's Y,” and stock phrases such as “supercharge,” “seamless,” “effortless,” and “10x.”

These rules govern authored Yak UI and documentation. Preserve user messages, titles, labels, handles, and quotes as supplied. Check light and dark surfaces, phone layout, zoom, keyboard focus, and the bounds of clickable controls. Use at least 12 CSS pixels and 4.5:1 contrast for functional text and 24 by 24 CSS pixels for interactive targets.

Run `node examples/viewer/test/regress.mjs` to check the viewer assets and exercise the synthetic board in Chrome.
