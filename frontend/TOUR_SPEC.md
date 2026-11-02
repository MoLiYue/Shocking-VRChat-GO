# 页内 Coachmark Tour（引导浮层）视觉与交互规范

> 面向开发落地的规范文档，**不含实现代码**。
> 硬性约束：禁止引入任何新库；全部视觉必须复用现有设计系统（`frontend/src/assets/global.css` 的 CSS 变量、玻璃拟态、`--bg-card`/`--border`/渐变、现有按钮 class `.btn` `.btn-primary` `.btn-ghost`）。
> 技术底座（现状已确认）：Vue 3 + TS，路由在 `frontend/src/main.ts`，根布局/侧边栏在 `frontend/src/App.vue`，i18n 通过 `useI18n().t('a.b.c')`（点号 key，支持 `{var}` 插值，locale 存 `localStorage.locale`）。

---

## 0. 组件形态与落地位置

- 单一全局组件 `frontend/src/components/TourGuide.vue`，挂载在 `App.vue` 的 `<main>` 之外（作为 `.shell` 的兄弟节点），确保遮罩覆盖侧边栏与内容区。
- 组件通过一个轻量组合式状态 `frontend/src/composables/useTour.ts`（纯 `ref`，不引库）驱动：`start()` / `next()` / `prev()` / `skip()` / `finish()` / `restart()`、`activeStep`、`isActive`。
- 组件层级：`z-index` 使用固定值 **9000**（高于侧边栏 `z-index:100`，见 `App.vue` 移动端 sidebar）。气泡卡片 `z-index: 9001`。
- 定位与滚动：每步切换时，先 `router.push(step.route)`，等一帧（`nextTick` + 一次 `requestAnimationFrame`）后再查询锚点并计算高亮框；锚点若不在视口内先 `scrollIntoView({ block: 'center', behavior: 'smooth' })`。

---

## 1. 遮罩 + 挖洞高亮框

采用「单层遮罩 + 高亮框投射巨型外阴影」方案（无需 SVG、无需第二层 DOM 打洞）。

**高亮框（highlight box）**
- 绝对定位在目标元素的包围盒上，尺寸 = 目标 `getBoundingClientRect()` 加内边距。
- 内边距（padding）：`--sp-2`（8px），四边一致。
- 圆角：`--radius-md`（12px）。目标本身是药丸按钮（如 `.btn`）时用 `--radius-full`；是整块卡片（`.card` / `.channel-section`）时用 `--radius-lg`（18px）。规则见 §3 尾部映射表。
- 关键样式（压暗周围、目标透亮）：
  ```
  box-shadow: 0 0 0 9999px rgba(0, 0, 0, 0.6);
  border: 1px solid var(--border-hover);
  border-radius: var(--radius-md);
  ```
- 呼吸描边（复用现有 glow，弱化处理）：叠加 `box-shadow: 0 0 0 9999px rgba(0,0,0,.6), var(--glow-sm);`，用一个 2s 的 `@keyframes` 在 `var(--glow-sm)` ↔ `var(--glow-md)` 间过渡，突出目标但不喧宾夺主。
- 过渡：位置/尺寸变化用 `transition: all var(--transition)`（`200ms cubic-bezier(0.4,0,0.2,1)`），让高亮框在步骤间平滑滑动。

**遮罩层本体**
- 一个铺满视口的透明捕获层（`position: fixed; inset: 0; z-index: 9000; background: transparent;`），仅用于拦截点击（点击遮罩空白处不关闭，避免误触；关闭只走「跳过」按钮）。真正的压暗由高亮框的 `9999px` 外阴影产生。
- 高亮框区域需 `pointer-events: none`，避免遮挡目标；若某步要求用户真正点击目标（如「点击添加参数」），则该步标记 `interactive: true`，此时高亮框区域放行点击，并监听目标事件自动 `next()`。

**减少动效**：尊重 `@media (prefers-reduced-motion: reduce)`，关闭呼吸动画与滑动过渡，仅保留静态高亮。

---

## 2. 气泡卡片（Tooltip Card）

玻璃拟态卡片，复用 `.card` 视觉语言但独立 class（`.tour-bubble`）以控制宽度与箭头。

- 容器：
  ```
  background: var(--bg-card);
  backdrop-filter: var(--blur);
  -webkit-backdrop-filter: var(--blur);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow);
  padding: var(--sp-5);
  width: 320px; max-width: calc(100vw - 2 * var(--sp-4));
  z-index: 9001;
  ```
- 结构（从上到下）：
  1. **步骤指示**：`第 {n} / {N} 步`（i18n `tour.progress`，`{n}`/`{total}` 插值）。字号 `--text-xs`，色 `--text-muted`，字母间距略紧。
  2. **标题**：字号 `--text-lg`，`font-weight: 700`，色 `--text`；可选 `.gradient-text` 用于第一/最后一步的欢迎/收尾标题。
  3. **说明正文**：字号 `--text-sm`，色 `--text-secondary`，`line-height: 1.6`。
  4. **安全提示条**（仅安全相关步骤显示）：复用 `StrengthLimit.vue` 的 `recommend-banner` 视觉——`background: var(--warning-surface); border: 1px solid rgba(251,191,36,0.25); border-radius: var(--radius-md); padding: var(--sp-3);` 左侧 `⚠️` 图标，文字 `--text-sm`。
  5. **按钮行**：`display:flex; gap: var(--sp-3); margin-top: var(--sp-4);`
     - 「跳过」：`.btn .btn-ghost .btn-sm`，靠左；文案 `tour.skip`。
     - 右侧一组（`margin-left:auto`）：「上一步」`.btn .btn-ghost`（首步禁用 `disabled`），「下一步」`.btn .btn-primary`；末步「下一步」文案切换为 `tour.done`（完成）。
- **箭头/指向**：气泡朝目标一侧伸出一个 8px 三角，用 `::before` 旋转 45° 的方块实现，`background: var(--bg-card); border` 取相邻两边的 `--border`，与卡片拼接。箭头位置随 §3 定位方向切换（top/bottom/left/right 四套）。
- **入场动效**：`opacity 0→1` + `translateY(8px)→0`，`150ms`，复用 `App.vue` 已有的 page-enter 曲线感（不新增库）。

---

## 3. 智能定位规则（相对目标元素，自动避让视口边缘）

输入：目标包围盒 `rect`（含 §1 的 `--sp-2` 内边距外扩后）、气泡尺寸 `bw×bh`、视口 `vw×vh`、间距常量 `GAP = --sp-3`（12px）、边缘安全距 `MARGIN = --sp-4`（16px）。

**首选方向顺序**：`bottom → top → right → left`（自然阅读优先在目标下方）。可被单步的 `placement` 字段覆盖为强制方向。

**可放置性判定**（逐个方向试算，取第一个满足者）：
- bottom：`rect.bottom + GAP + bh <= vh - MARGIN`
- top：`rect.top - GAP - bh >= MARGIN`
- right：`rect.right + GAP + bw <= vw - MARGIN`
- left：`rect.left - GAP - bw >= MARGIN`

**主轴坐标**：按方向贴合目标外侧并留 `GAP`。

**交叉轴对齐 + 夹紧（clamp）**：
- 上/下方向：气泡水平中心对齐目标中心 `x = rect.centerX - bw/2`，随后 `x = clamp(x, MARGIN, vw - bw - MARGIN)`。
- 左/右方向：气泡垂直中心对齐目标中心 `y = rect.centerY - bh/2`，随后 `y = clamp(y, MARGIN, vh - bh - MARGIN)`。
- 箭头位置需在夹紧后**重新指回目标中心**（箭头沿交叉轴定位到 `targetCenter - bubbleStart`，并限制在气泡内 `[12px, size-12px]`），避免夹紧后箭头脱离目标。

**兜底**：四方向都放不下（超小视口）→ 居中显示气泡（`position: fixed; 居中`），隐藏箭头，高亮框照常存在。

**侧边栏锚点特例**：指向 `App.vue` 侧边栏项时，移动端（`max-width:768px`）侧边栏为抽屉且默认 `translateX(-100%)`。移动端进入 tour 时应先展开侧边栏（临时置 `sidebarOpen`/去 `closed`），并把这些步骤的首选方向设为 `right`。

**目标缺失容错**：锚点查询为空（页面未加载/元素条件渲染未出现）→ 轮询重试（每 `100ms`，最多 `1.5s`）；仍失败则跳过该步并 `console.warn`，不阻塞整条路线。

**圆角映射表（高亮框 border-radius）**：

| 目标类型（选择器语义） | 圆角变量 |
|---|---|
| 药丸按钮 `.btn` / `.qr-tab` / `.adj-btn` | `--radius-full` / `--radius-md` |
| 输入框 `input` / `select` | `--radius-md` |
| 滑块 `input[type=range]` 容器 | `--radius-md` |
| 卡片 `.card` / `.channel-section` / `.stat-card` | `--radius-lg` |
| 侧边栏项 `.nav-link` | `--radius-md` |

---

## 4. `data-tour` 锚点契约清单

开发需在对应元素上新增 `data-tour="<值>"` 属性（不改变现有 class/结构，仅加属性）。锚点值全局唯一。

| `data-tour` 值 | 归属文件 | 落点元素（现状描述） | 用途 |
|---|---|---|---|
| `nav-params` | `App.vue` | 侧边栏「参数管理」`router-link`（`to="/params"`） | 指引进入参数管理 |
| `nav-strength` | `App.vue` | 侧边栏「强度设置」`router-link`（`to="/strength"`） | 指引进入强度设置 |
| `nav-dashboard` | `App.vue` | 侧边栏「Dashboard」`router-link`（`to="/dashboard"`） | 指引回到 Dashboard |
| `tour-restart` | `App.vue` | 侧边栏新增「重新查看引导」按钮（放在 `lang-switcher` 上方或 `shutdown-btn` 附近） | 手动重启 tour 入口 |
| `param-channel-a` | `ParamMap.vue` | Channel A 的 `.channel-section` | 概览通道 A 配置区 |
| `param-add` | `ParamMap.vue` | Channel A 的 `.add-form`（含前缀 label + input + select + 「+」按钮） | 添加参数（可交互步骤） |
| `param-add-input` | `ParamMap.vue` | Channel A `.add-input`（`v-model="newPathA"`） | 精确指向路径输入框（可选细化步骤） |
| `param-mode-select` | `ParamMap.vue` | Channel A `.add-select`（`v-model="newModeA"`）**或**默认模式 `.setting select` | 选择工作模式 |
| `param-save` | `ParamMap.vue` | Channel A `.save-row` 的 `.btn-primary`（💾 保存） | 保存并热重载提示 |
| `strength-slider` | `StrengthLimit.vue` | Channel A 强度 `input[type=range]`（`v-model="limitA"`）所在 `.slider-row` | 调节强度上限（安全提示步骤） |
| `strength-value` | `StrengthLimit.vue` | Channel A `.value-row`（− / number / +） | 微调数值 |
| `strength-overlimit` | `StrengthLimit.vue` | Channel A 超限额度 `.field`（第二个 field） | 说明超限额度（可选） |
| `qr-code` | `Dashboard.vue` | QR 卡片内 `<QrCode>` 组件容器（`.card` 内） | 指引扫码连接设备 |
| `qr-tabs` | `Dashboard.vue` | `.qr-tabs`（V4 / V3 切换） | 说明协议版本选择（可选） |
| `dashboard-status` | `Dashboard.vue` | `.stats-row` 第一个 `.stat-card`（设备状态） | 完成步：查看连接状态 |
| `mode-select` | `SetupWizard.vue` **或** `ParamMap.vue` | 模式选择控件（向导 step 或参数默认模式 select） | 可选的「模式选择」分支步骤 |

> 注：`QrCode.vue` 组件需能接收并透传 `data-tour` 到其根元素（或在 `Dashboard.vue` 用包裹 `div[data-tour="qr-code"]` 承载，避免改组件）。推荐用外层包裹 `div` 承载，零侵入。

---

## 5. 引导路线（步骤定义）

主路线：**参数 → 强度 → 二维码 → 完成**，中间含一个可选「模式选择」提示。每步字段：`id` / `anchor`（`data-tour` 值）/ `route` / `placement`（可选，默认 bottom）/ `interactive`（可选）/ `safety`（是否安全提示）/ i18n 文案 key。

| # | id | anchor | route | placement | 文案要点（i18n key） | 安全提示 |
|---|---|---|---|---|---|---|
| 1 | `welcome` | 无（居中兜底） | `/dashboard` | center | 欢迎语 + 「2 分钟带你完成配置」（`tour.welcome.*`） | 否 |
| 2 | `goto-params` | `nav-params` | 当前 | right | 「先去参数管理」指引点击侧边栏（`tour.gotoParams.*`），`interactive` 可选 | 否 |
| 3 | `add-param` | `param-add` | `/params` | top | 「在这里添加 OSC 参数，前缀已自动补全，支持通配符 `*`」（`tour.addParam.*`） | 否 |
| 4 | `pick-mode` | `param-mode-select` | `/params` | top | 「选择工作模式：距离/电击/触摸/Combo」＋一句各模式差异（`tour.pickMode.*`）（可选步） | 否 |
| 5 | `save-param` | `param-save` | `/params` | top | 「保存后自动热重载，设备连接不断开」（`tour.saveParam.*`） | 否 |
| 6 | `goto-strength` | `nav-strength` | 当前 | right | 「接着设置强度上限」（`tour.gotoStrength.*`） | 否 |
| 7 | `set-strength` | `strength-slider` | `/strength` | bottom | 「从低值开始（建议 20~40），逐步提高；实际输出取程序与 APP 的较小值」（`tour.setStrength.*`） | **是** |
| 8 | `goto-qr` | `nav-dashboard` | 当前 | right | 「回到 Dashboard 连接设备」（`tour.gotoQr.*`） | 否 |
| 9 | `scan-qr` | `qr-code` | `/dashboard` | left | 「用郊狼 APP 的 Socket 控制扫码；推荐 V4 协议」（`tour.scanQr.*`） | 否 |
| 10 | `finish` | `dashboard-status` | `/dashboard` | bottom | 「连接成功后这里会变绿。随时可在侧边栏『重新查看引导』重看本教程」（`tour.finish.*`） | **是**（含使用安全须知一句提醒 + 链接到安全须知） |

**安全提示文案要求**：
- 第 7 步（强度）安全条：强调「循序渐进、从低到高、以自身承受为准」。
- 第 10 步（完成）安全条：一句「使用前请阅读并同意安全须知」，并可放一个指向安全须知文档/页面的链接（复用 `a` 链接样式）。

**分支：模式选择**：第 4 步 `pick-mode` 为可选。若产品希望更完整，可在首次向导 `SetupWizard.vue`（路由 `/setup`）内联同款气泡指向其模式选择控件（anchor `mode-select`）；主 tour 保持第 4 步的轻量提示即可，避免与向导重复。

---

## 6. 完成状态与入口（localStorage + 重看入口）

**存储**
- key：`tour_done`，value：`'1'`（存在即视为已完成/已跳过）。
- 版本化（可选，推荐）：`tour_done` 存写入时的 tour 版本号（如 `'v1'`），便于将来教程更新后重新触发一次；比较不相等则允许再次自动弹出。

**自动触发规则（只弹一次）**
- 在 `App.vue` 挂载后判断：`!localStorage.getItem('tour_done')` 时自动 `start()`。
- 与首配向导互斥：若应用会跳转 `/setup`（首次配置），tour **不**与向导同时出现；建议在向导「保存并启动」完成、落到 `/dashboard` 后再自动触发 tour（或直接把 tour 作为向导后的可选下一步）。
- 「跳过」和「完成（末步下一步）」都写入 `tour_done`，此后不再自动弹。

**手动重看入口**
1. 侧边栏：新增按钮 `data-tour="tour-restart"`，文案 `nav.tourRestart`（如「🧭 重新查看引导」），点击调用 `restart()`：清理内部步骤指针并 `start()`（不需要清除 `tour_done`，`restart` 直接强制启动）。放置在 `lang-switcher` 与 `shutdown-btn` 之间，复用 `shutdown-btn` 同款 ghost 风格 class。
2. 设置页 `Settings.vue`：在页面底部或「配置导入/导出」卡片附近，加一个 `.btn .btn-ghost`，文案 `settings.tourRestart`（如「🧭 重新查看新手引导」），同样调用 `restart()`。

---

## 7. i18n 文案键结构（需同时补 `zh.ts` 与 `en.ts`）

新增顶层命名空间 `tour`，并在 `nav`/`settings` 各补一个重看入口 key。结构示例（值为占位，落地时填写正式文案）：

```
tour: {
  progress: '第 {n} / {total} 步',      // en: 'Step {n} / {total}'
  skip: '跳过',
  prev: '上一步',
  next: '下一步',
  done: '完成',
  welcome:      { title: '', body: '' },
  gotoParams:   { title: '', body: '' },
  addParam:     { title: '', body: '' },
  pickMode:     { title: '', body: '' },
  saveParam:    { title: '', body: '' },
  gotoStrength: { title: '', body: '' },
  setStrength:  { title: '', body: '', safety: '' },
  gotoQr:       { title: '', body: '' },
  scanQr:       { title: '', body: '' },
  finish:       { title: '', body: '', safety: '', safetyLink: '' },
}
nav.tourRestart: '🧭 重新查看引导'
settings.tourRestart: '🧭 重新查看新手引导'
```

- 所有气泡文字必须走 `t('tour.*')`，禁止硬编码中文/英文。
- 进度用插值：`t('tour.progress', { n: current+1, total })`。
- 组件内切换语言（`setLocale`）后文案应即时更新（`t` 依赖响应式 `currentLocale`，重新读取即可）。

---

## 8. 无障碍与健壮性要点

- 气泡卡片容器 `role="dialog"` + `aria-modal="true"`，标题用 `aria-labelledby` 关联。
- 键盘：`Esc` = 跳过；`←/→` 或 `Enter` = 上一步/下一步；焦点进入 tour 时移到「下一步」按钮，离开时归还。
- 焦点陷阱仅限气泡内按钮，遮罩不抢占页面滚动之外的语义。
- 窗口 `resize`/`scroll` 时防抖重算高亮框与气泡位置（`requestAnimationFrame` 节流，不新增依赖）。
- 路由被用户手动改变（点了别处）时：以「当前路由是否等于该步 `route`」为准，不一致则自动 `router.push` 回该步路由或就近重定位。

---

## 9. 落地检查清单（DoD）

- [ ] 新增 `TourGuide.vue` + `useTour.ts`，无第三方依赖。
- [ ] 在列出的元素上补齐全部 `data-tour` 锚点（§4）。
- [ ] `App.vue` 挂载 `TourGuide`，加「重新查看引导」入口；`Settings.vue` 加重看按钮。
- [ ] `zh.ts` / `en.ts` 补齐 `tour.*`、`nav.tourRestart`、`settings.tourRestart`。
- [ ] 全部视觉仅用现有 CSS 变量与按钮 class；无硬编码颜色/间距（除既定的 `rgba(0,0,0,.6)` 遮罩与 `9999px` 阴影）。
- [ ] `prefers-reduced-motion` 生效；移动端侧边栏锚点可正确高亮。
- [ ] 首次自动弹一次并写 `tour_done`；重看入口可再次触发。
- [ ] 构建校验通过：`vue-tsc --noEmit` 与 `vite build`。
