# Cursor Plugin Management UI

## 1. Product context

The page is an operational surface served directly by the Cursor plugin. It must remain usable without CPA Manager Plus or CLIProxyAPI frontend source changes. The primary users are self-hosting operators who need to inspect Cursor account health and prevent selected models from being advertised or executed.

## 2. Direction

Preserve the compact, neutral system-tool character of CLIProxyAPI's own plugin resource examples. The memorable element is the explicit separation between authoritative subscription quota and local estimated usage: unknown data is shown honestly, never converted into a misleading progress bar.

## 3. Tokens

- Typeface: system UI for content; system monospace for model IDs and metrics.
- Canvas: `--canvas`; raised panel: `--surface`; subtle panel: `--surface-muted`.
- Text: `--text`; secondary text: `--muted`; border: `--border`.
- Accent: `--accent`; success: `--success`; warning: `--warning`; danger: `--danger`.
- Spacing follows a 4 px base unit: 4, 8, 12, 16, 24, 32.
- Radius: 8 px controls, 12 px cards. Shadow is limited to the account card.

## 4. Layout and responsiveness

- A centered shell with a readable maximum width and document scrolling.
- Header and credential panel appear first; account cards follow.
- Account summary becomes a single column below 720 px.
- Model choices use a responsive grid and never force horizontal scrolling.

## 5. Primitives and states

- `Panel`: default and warning variants.
- `Field`: label, password input, helper text, focus-visible state.
- `Button`: primary, secondary, loading, disabled, focus-visible states.
- `Metric`: label and monospace value.
- `MetricSection`: heading, explanatory copy, and an intrinsic metric grid. Logical request outcomes, host scheduler attempts, and checkpoint telemetry are separate sections and are never merged.
- `ModelOption`: checked, unchecked, keyboard-focus, disabled states.
- `Notice`: information, success, and error states with `role=status` or `role=alert`.
- `LanguageSelect`: native select with Chinese and English options, keyboard focus, and persisted preference.

## 6. Interaction

- The management key remains in page memory only and is never persisted.
- “加载状态” fetches the authenticated plugin status endpoint.
- “全部禁用 / Disable all” selects every available model for the current account but does not persist until the explicit save action.
- “保存设置 / Save settings” submits only the selected account and model IDs.
- Buttons expose loading and disabled states; completion is announced to assistive technology.
- The initial language follows a previously saved preference, then the browser language, with English as the fallback.
- Changing language immediately updates static copy, dynamic account cards, notices, the document title, and the document language. Only the language preference is persisted.
- Account labels, credential filenames, and model IDs are host-provided identity data and remain verbatim; only interface copy is localized.
- No decorative animation. Reduced-motion users receive the same instantaneous state changes.

## 7. Accessibility

- Semantic headings, labels, fieldsets, legends, and buttons.
- Visible keyboard focus and minimum 44 px interactive height.
- Status is not conveyed by color alone.
- The account status pill represents the plugin's latest terminal request outcome. Before the first local outcome, successful credential loading and model discovery establish readiness; stale CLIProxyAPI scheduler errors stay in the separately labelled host metrics and do not override that readiness. Explicit disabled or inactive host state is still respected.
- The language selector uses a native labelled control, and `<html lang>` always matches the displayed language.
- Chinese copy uses natural line breaks; model IDs may break only at safe punctuation.
- English copy uses concise operational language and remains readable without horizontal scrolling.

## 8. Accepted debt

- Cursor does not publish a stable OAuth subscription remaining-quota API. The page therefore displays an unavailable state and local estimated usage instead of a fabricated remainder.
- CLIProxyAPI success and failure counters represent scheduler attempts, including retries and fallbacks. They are displayed as host attempts, never as user request totals.
- Logical request outcomes and Token estimates are process-local and reset when the plugin process restarts. Request outcomes come from the host lifecycle completion callback; the plugin defensively deduplicates recent completion IDs in two independent 15-minute, 8 MiB Bloom windows. At the design load of up to one million completions per window, a two-window lookup has a theoretical false-positive probability below `6e-8`. A false positive skips one local terminal sample, leaving the previous latest outcome unchanged. Token estimates accumulate per plugin executor run.
- The standalone page asks for the management key because plugin browser resources are intentionally unauthenticated by the host and cannot safely mutate state by themselves.

## Reference

Visual grammar is extracted from CLIProxyAPI's `examples/plugin/host-callback-auth-files` resource page: system typography, restrained neutral surfaces, direct operational copy, and no external assets.
