// The icon set.
//
// Inline SVG built with createElementNS, for two reasons that are both hard
// requirements rather than preferences:
//
//   1. The Content-Security-Policy sets `font-src 'none'`, so an icon font is
//      not available, and `img-src 'self'` with no build step means a sprite
//      file would be another request to hand-maintain. A path string compiled
//      into a node needs neither.
//
//   2. Nothing here parses markup. `innerHTML = "<svg…>"` is the obvious way
//      to do this and is exactly what the renderer's guard forbids, because
//      the moment it exists somebody passes a server string through it.
//
// Every icon is 16×16 on a 16-unit grid, stroked in currentColor, so an icon
// is the colour and weight of the text beside it and inherits its own state.
// They are decoration: each one sits next to a word, and is marked
// aria-hidden so a screen reader reads the word once rather than twice.

const NS = "http://www.w3.org/2000/svg";

// A name that is not here draws nothing rather than a placeholder box: a
// missing icon should disappear, not make a page look broken.
// ICONS is the whole vocabulary, as a list rather than a keyed object.
//
// A list because nothing in this bundle enumerates an object's keys: the
// guard that forbids Object.keys exists so a server payload can never be
// walked, and a local constant is not worth an exception to a rule whose
// value is that it has none. It is also the shape every other table in the
// source uses — an allowlist is iterated, never introspected.
//
// Kept deliberately small. Every entry earns its place by labelling
// something a reader must find quickly; an icon on everything is an icon on
// nothing.
const ICONS = Object.freeze([
  // Destinations
  Object.freeze({ name: "live", paths: ["M8 1.5a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13Z", "M8 5.5a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5Z"] }),
  Object.freeze({ name: "projects", paths: ["M1.5 4.5 A1 1 0 0 1 2.5 3.5h3.2l1.3 1.6h5.5a1 1 0 0 1 1 1v6.4a1 1 0 0 1-1 1h-10a1 1 0 0 1-1-1Z"] }),
  Object.freeze({ name: "runs", paths: ["M2 4h12", "M2 8h12", "M2 12h7"] }),
  Object.freeze({ name: "compare", paths: ["M5.5 2v12", "M10.5 2v12", "M2 5.5h3.5", "M10.5 10.5H14"] }),
  Object.freeze({ name: "evidence", paths: ["M8 1.8 2.8 4v4c0 3.2 2.2 5.5 5.2 6.2 3-0.7 5.2-3 5.2-6.2V4Z", "M6 8l1.6 1.6L10.4 6.8"] }),
  Object.freeze({ name: "overview", paths: ["M2 2.5h5v5H2Z", "M9 2.5h5v3H9Z", "M9 7.5h5v6H9Z", "M2 9.5h5v4H2Z"] }),
  Object.freeze({ name: "trace", paths: ["M2 3.5h7", "M4 7h8", "M6 10.5h6", "M2 3.5v3.5h2", "M4 7v3.5h2"] }),
  Object.freeze({ name: "promotion", paths: ["M8 13.5V3", "M4.2 6.8 8 3l3.8 3.8"] }),
  Object.freeze({ name: "manage", paths: ["M8 10.2a2.2 2.2 0 1 0 0-4.4 2.2 2.2 0 0 0 0 4.4Z", "M12.9 10a1.1 1.1 0 0 0 .22 1.21l.04.04a1.33 1.33 0 1 1-1.88 1.88l-.04-.04a1.1 1.1 0 0 0-1.21-.22 1.1 1.1 0 0 0-.67 1v.11a1.33 1.33 0 1 1-2.67 0v-.06a1.1 1.1 0 0 0-.72-1 1.1 1.1 0 0 0-1.21.22l-.04.04a1.33 1.33 0 1 1-1.88-1.88l.04-.04a1.1 1.1 0 0 0 .22-1.21 1.1 1.1 0 0 0-1-.67h-.11a1.33 1.33 0 1 1 0-2.67h.06a1.1 1.1 0 0 0 1-.72 1.1 1.1 0 0 0-.22-1.21l-.04-.04a1.33 1.33 0 1 1 1.88-1.88l.04.04a1.1 1.1 0 0 0 1.21.22h.05a1.1 1.1 0 0 0 .67-1v-.11a1.33 1.33 0 1 1 2.67 0v.06a1.1 1.1 0 0 0 .67 1 1.1 1.1 0 0 0 1.21-.22l.04-.04a1.33 1.33 0 1 1 1.88 1.88l-.04.04a1.1 1.1 0 0 0-.22 1.21v.05a1.1 1.1 0 0 0 1 .67h.11a1.33 1.33 0 1 1 0 2.67h-.06a1.1 1.1 0 0 0-1 .67Z"] }),
  // Verdict and severity. These stand beside a word, never instead of one.
  Object.freeze({ name: "allow", paths: ["M3.2 8.4 6.4 11.6 12.8 4.8"] }),
  Object.freeze({ name: "block", paths: ["M8 2.2a5.8 5.8 0 1 0 0 11.6 5.8 5.8 0 0 0 0-11.6Z", "M4.2 4.2l7.6 7.6"] }),
  Object.freeze({ name: "flag", paths: ["M8 2.6 14.4 13.4H1.6Z", "M8 6.6v3", "M8 11.6h.01"] }),
  // Affordances
  Object.freeze({ name: "search", paths: ["M7.2 12.4a5.2 5.2 0 1 0 0-10.4 5.2 5.2 0 0 0 0 10.4Z", "M14 14l-3.1-3.1"] }),
  Object.freeze({ name: "close", paths: ["M4 4l8 8", "M12 4l-8 8"] }),
  Object.freeze({ name: "chevron", paths: ["M6 3.5 10.5 8 6 12.5"] }),
  Object.freeze({ name: "empty", paths: ["M2.5 5.5h11v8h-11Z", "M2.5 5.5 8 9.5l5.5-4"] }),
  Object.freeze({ name: "clock", paths: ["M8 2.4a5.6 5.6 0 1 0 0 11.2A5.6 5.6 0 0 0 8 2.4Z", "M8 5.2V8l2 1.4"] }),
]);

// icon returns one decorative glyph.
//
// Returns an empty, harmless node for an unknown name rather than throwing:
// a view that asks for an icon that has been renamed should lose the icon,
// not the page.
export function icon(name, className) {
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("class", className ? `icon ${className}` : "icon");
  svg.setAttribute("viewBox", "0 0 16 16");
  // Decoration beside a word. The word is the accessible name, and a screen
  // reader that read both would say everything twice.
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("focusable", "false");

  let paths = null;
  for (const entry of ICONS) {
    if (entry.name === name) {
      paths = entry.paths;
      break;
    }
  }
  if (paths === null) {
    return svg;
  }
  for (const d of paths) {
    const node = document.createElementNS(NS, "path");
    node.setAttribute("d", d);
    svg.append(node);
  }
  return svg;
}

// iconNames is the vocabulary, for the guard that keeps every name a view
// references real.
export const iconNames = Object.freeze(ICONS.map((entry) => entry.name));
