// DOM construction, and the reason the renderer is safe.
//
// This module knows nothing about Trustvian: no run, no observation, no
// route. It is the bottom of the stack, and everything above it builds nodes
// through these two functions.
//
// **Every node is created and filled with textContent.** No markup is ever
// parsed, anywhere in the bundle, so a value the control plane returns cannot
// become executable DOM however it is spelled. That is not a convention the
// reviewer has to remember — `innerHTML`, `outerHTML`, `insertAdjacentHTML`,
// `document.write` and `eval` are all scanned for and refused by a guard, and
// keeping the forbidden property out of the source entirely is what makes the
// scan absolute instead of a rule with exceptions.

// element builds one node with an optional class and text.
export function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) {
    node.className = className;
  }
  if (text !== undefined && text !== null) {
    node.textContent = String(text);
  }
  return node;
}

// clear empties a node.
//
// replaceChildren() with no arguments, rather than innerHTML = "". Same
// result, and it keeps the forbidden property out of the source so the guard
// stays absolute rather than carrying an exception for the one safe use.
export function clear(node) {
  node.replaceChildren();
}

// byID is the one lookup the application uses.
export const byID = (id) => document.getElementById(id);
