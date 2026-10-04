// The colour-scheme preference: Light, Dark, or whatever the system says.
//
// This is the one value the bundle writes to browser storage (ADR 0061), and
// this module and the pre-paint script `core/theme-boot.js` are the only two
// files that touch it. It is presentation state about the reader's eyes, not
// platform state: losing it costs a click, and nothing the control plane holds
// is ever cached beside it.
//
// "System" is stored as the absence of a value, so a reader who never chose —
// or who chose System — keeps following the operating system when it changes,
// and nothing is written on a first visit.
//
// Every storage access is guarded. Storage can be absent, blocked by policy,
// or throw on access in a private window; each of those must render the
// system scheme rather than an error, and a write that fails still applies the
// choice for the rest of the session.

export const THEME_STORAGE_KEY = "trustvian.theme";

export const THEME_PREFERENCES = Object.freeze(["system", "light", "dark"]);

// The two values that may be stored. "system" is never stored: it is removal.
const STORED = Object.freeze(["light", "dark"]);

// browserStorage returns the window's storage, or null where reaching it
// throws (a blocked or sandboxed context does exactly that).
export function browserStorage(win) {
  try {
    return win && win.localStorage ? win.localStorage : null;
  } catch (ignored) {
    return null;
  }
}

// readThemePreference returns the stored choice, or "system".
//
// A value this module would not have written — an older format, a hand edit —
// is treated as no choice at all rather than trusted.
export function readThemePreference(storage) {
  if (storage === null || storage === undefined) {
    return "system";
  }
  try {
    const stored = storage.getItem(THEME_STORAGE_KEY);
    return STORED.includes(stored) ? stored : "system";
  } catch (ignored) {
    return "system";
  }
}

// writeThemePreference records a choice, and reports whether it will survive
// a reload. An unknown preference is refused rather than coerced.
export function writeThemePreference(storage, preference) {
  if (!THEME_PREFERENCES.includes(preference)) {
    return false;
  }
  if (storage === null || storage === undefined) {
    return false;
  }
  try {
    if (preference === "system") {
      storage.removeItem(THEME_STORAGE_KEY);
    } else {
      storage.setItem(THEME_STORAGE_KEY, preference);
    }
    return true;
  } catch (ignored) {
    return false;
  }
}

// resolveTheme is the scheme a preference produces under a system setting.
export function resolveTheme(preference, systemPrefersDark) {
  if (preference === "light" || preference === "dark") {
    return preference;
  }
  return systemPrefersDark ? "dark" : "light";
}

// applyThemePreference marks the root element. System removes the mark, which
// hands the decision back to the stylesheet's prefers-color-scheme block.
export function applyThemePreference(root, preference) {
  if (preference === "light" || preference === "dark") {
    root.setAttribute("data-theme", preference);
  } else {
    root.removeAttribute("data-theme");
  }
}
