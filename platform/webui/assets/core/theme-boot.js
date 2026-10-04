// Pre-paint theme application (task 097, ADR 0061).
//
// A classic script, loaded synchronously from <head> before the stylesheets
// apply, so a reader who chose Dark never sees a light frame first. It cannot
// import core/theme.js — a module is deferred, which is exactly the flash this
// exists to prevent — so it repeats the two facts it needs: the key and the
// two values that may be stored. A test runs both and asserts they agree.
//
// External rather than inline: the Content-Security-Policy allows scripts
// from 'self' only, and an inline block would need 'unsafe-inline'.
(function applyStoredTheme() {
  try {
    var stored = window.localStorage.getItem("trustvian.theme");
    if (stored === "light" || stored === "dark") {
      document.documentElement.setAttribute("data-theme", stored);
    }
  } catch (ignored) {
    // Storage blocked or absent: the operating system's scheme applies.
  }
})();
