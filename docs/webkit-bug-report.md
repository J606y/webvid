# WebKit bug report — 提交用文本（bugs.webkit.org / Feedback Assistant 通用）

提交时把下面英文部分整段复制；附件带上同目录的 `webkit-blur-repro.html`。
Bugzilla 建议填法：Product = WebKit，Component = Layout and Rendering（或 Compositing），
Hardware = iPhone / iOS。Feedback Assistant 在 iPhone 上提交可自动附系统诊断，对 beta 更有用。

---

**Title:**
Inserting an overlay (nested fixed + overflow:auto, or backdrop-filter) over large
`filter: blur()` fixed layers stalls the main thread for 10+ seconds on iOS

**Summary:**
On iOS, when a page contains large fixed-position decorative layers using
`filter: blur(120px)` (a common "aurora / glassmorphism" background pattern), inserting
a typical modal-dialog overlay as a direct child of `<body>` causes a synchronous stall
of roughly 10–12 seconds. The same page opens the same overlay instantly in
Chrome/Edge/Firefox on desktop and in Chrome on Android (tested on a much slower
Snapdragon 865 device — smooth, while an A16 iPhone stalls).

**Environment:**
- iPhone (A16), iOS 27 beta 3, Safari Version/27.0 (WebKit 605.1.15)
- Reproduces in every iOS browser (all WebKit); not reproducible in Blink/Gecko
- User agent: Mozilla/5.0 (iPhone; CPU iPhone OS 18_7 like Mac OS X) AppleWebKit/605.1.15
  (KHTML, like Gecko) Version/27.0 Mobile/15E148 Safari/604.1

**Steps to reproduce (attached webkit-blur-repro.html):**
1. Open the attached single-file test page on an iPhone.
2. Tap button **A** — it appends an overlay with the DOM structure used by common
   dialog libraries (full-viewport `position: fixed` mask with `overflow: auto`,
   containing a nested `position: fixed` wrapper that also has `overflow: auto`).
   No backdrop-filter involved.
3. Observe the reported "longest main-thread stall" (measured via rAF frame gap).
4. Tap button **B** — same overlay but the trigger is `backdrop-filter: blur(8px)`
   on the mask, with no `overflow: auto` anywhere. Same stall.
5. Tap button **C** to hide the blurred background layers, then repeat A/B — both
   overlays now open instantly (< 100 ms), demonstrating the interaction.

**Actual results:**
- Main thread blocked for multiple seconds (in our production app we measured a
  12,049 ms rAF gap on a small page; the repro shows the same order of magnitude).
  The screen freezes and taps are ignored until the stall ends.
- A CSS transform keyframe animation (compositor-driven spinner) keeps running during
  the stall in the body-child case, indicating main-thread work rather than GPU work.
- Additional observation: if the identical dialog subtree is inserted deep inside the
  app's DOM instead of as a `<body>` child, the main thread stays responsive
  (rAF gap ~37 ms) but rendering/hit-testing freezes for a similar duration instead —
  suggesting the same expensive re-rasterization happens on the compositing side.

**Expected results:**
Inserting an unrelated overlay should not force synchronous re-rasterization of
already-composited filtered layers. The blurred layers' output should remain cached
on the GPU (this is the observed Blink behavior: blur computed on GPU and reused).

**Notes / analysis:**
- Two independent triggers were bisected on-device: (1) the nested
  fixed+overflow:auto overlay structure — used verbatim by popular Vue/React dialog
  libraries (e.g. Element Plus `el-dialog`), and (2) any `backdrop-filter` element
  appearing over the blurred layers. Either alone is sufficient.
- Cost scales with the blurred layers: 4 circles of ~40–60 vw with
  `filter: blur(120px)` at 3× DPR. Hiding them (`display: none`) removes the stall
  entirely; replacing the blur with pure radial-gradients also removes it.
- The blurred layers are transform-animated (slow drift). Not yet verified whether
  static blurred layers also trigger the stall.
- Real-world impact: any glassmorphism-style site combining a large blurred
  background with modal dialogs appears to hang for ~10 s on iOS every time a
  dialog opens; the same sites are smooth on Android/desktop.

---

提交渠道备忘：
1. **bugs.webkit.org** — 注册后 New Bug → Product: WebKit。工程师直接可见，可贴附件。
2. **Feedback Assistant**（feedbackassistant.apple.com 或 iPhone 上的反馈 App）——
   beta 系统上提交会自动附 sysdiagnose，Apple 对 beta 回归的处理优先级更高。
   分类选 Safari → 网页渲染/性能。
提交后把 bug 编号记下来，方便日后跟进/引用。
