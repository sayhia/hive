// Link-click handling for HTML injected into the reader (article body, AI
// summary). Extracted from papr's Reader.tsx so the Reader body and the AI
// drawer share one handler. Pure DOM + the Wails browser bridge.
//
// In-page anchor links (footnotes, tables of contents) scroll to their target
// within the reader; everything else opens in the external browser — a bare <a>
// click would otherwise navigate the webview away from the app entirely (or, for
// a fragment link, to a bogus `app://…#frag`).
//
// papr used Tauri's `@tauri-apps/plugin-opener` `openUrl`; here we call the
// Wails runtime `Browser.OpenURL`.

import { Browser } from "@wailsio/runtime";

/** Decode a URL fragment, tolerating a malformed `%` escape. A real-world anchor
 *  can carry a literal percent (`#100%-growth`, `#section-50%`), which is not a
 *  valid escape sequence — `decodeURIComponent` throws `URIError` on it. The
 *  bare value still works as an `id` lookup, so fall back to it rather than
 *  letting the throw escape the click handler and kill the link. */
function decodeFragment(frag: string): string {
  try {
    return decodeURIComponent(frag);
  } catch {
    return frag;
  }
}

/** Pull the in-page fragment out of a link click, or null if it isn't one.
 *
 *  Two shapes count as in-page: a bare `#frag` href, and — because the body HTML
 *  is sanitized with the article's URL as the rewrite base — an absolute
 *  `https://site/article#frag` that resolves to the very article being read.
 *  `sourceUrl` is the article's own URL, used to recognise that second case. */
function inPageFragment(raw: string, sourceUrl: string | null): string | null {
  if (raw[0] === "#") return decodeFragment(raw.slice(1));
  if (!sourceUrl) return null;
  try {
    const u = new URL(raw);
    const b = new URL(sourceUrl);
    if (u.hash && u.origin === b.origin && u.pathname === b.pathname) {
      return decodeFragment(u.hash.slice(1));
    }
  } catch {
    /* not a parseable absolute URL — treat as external */
  }
  return null;
}

/** Build a click handler for links inside injected HTML. In-page anchors scroll
 *  within the reader; everything else opens in the external browser. */
export function makeLinkClickHandler(sourceUrl: string | null) {
  return (e: MouseEvent) => {
    const link = (e.target as HTMLElement).closest("a");
    if (!link) return;
    const raw = link.getAttribute("href");
    if (!raw) return;
    e.preventDefault();

    const hash = inPageFragment(raw, sourceUrl);
    if (hash != null) {
      if (hash === "") return; // bare `#` — no element to reach
      const root = link.closest(".article-body, .ai-prose");
      // getElementById can't be scoped to the body, so match by id or the legacy
      // `<a name>` form within the rendered content.
      const target = root?.querySelector(
        `[id="${CSS.escape(hash)}"], a[name="${CSS.escape(hash)}"]`,
      );
      const reduce =
        document.documentElement.dataset.reduceMotion === "true" ||
        window.matchMedia("(prefers-reduced-motion: reduce)").matches;
      target?.scrollIntoView({ behavior: reduce ? "auto" : "smooth", block: "start" });
      return;
    }

    Browser.OpenURL(link.href).catch(() => {});
  };
}
