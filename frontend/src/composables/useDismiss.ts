// Outside-click / Escape dismissal for floating popovers (context menus,
// pickers, share menus). Every popover reimplemented the same logic: a
// `mousedown` listener that closes on a click outside `el`, an Escape
// `keydown` listener, and a `setTimeout(0)` so the very click/keypress that
// opened the popover does not immediately close it. Centralised here.
//
// Ported from papr's hooks/useDismiss.ts (React `useEffect` → Vue
// `onMounted`/`onBeforeUnmount`). Takes a Vue template ref to the popover root.

import { onBeforeUnmount, onMounted, type Ref } from "vue";

interface Options {
  /** Also dismiss when focus leaves the popover subtree (Tab past the last
   *  control). A null relatedTarget is ignored — left to the click/Escape
   *  paths — since focus falling to <body> is not a deliberate move out. */
  onFocusOut?: boolean;
}

/** Dismiss `elRef`'s popover on an outside click, Escape, or (optionally) a
 *  Tab out of its subtree. */
export function useDismiss(
  elRef: Ref<HTMLElement | null | undefined>,
  onClose: () => void,
  { onFocusOut = false }: Options = {},
) {
  let tm = 0;
  const onDown = (e: MouseEvent) => {
    if (!elRef.value?.contains(e.target as Node)) onClose();
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key === "Escape") onClose();
  };
  const onBlur = (e: FocusEvent) => {
    const next = e.relatedTarget as Node | null;
    if (next && !elRef.value?.contains(next)) onClose();
  };

  onMounted(() => {
    // Defer subscription a tick so the opening click/keypress doesn't dismiss.
    tm = window.setTimeout(() => {
      document.addEventListener("mousedown", onDown);
      window.addEventListener("keydown", onKey);
      if (onFocusOut) document.addEventListener("focusout", onBlur);
    }, 0);
  });

  onBeforeUnmount(() => {
    window.clearTimeout(tm);
    document.removeEventListener("mousedown", onDown);
    window.removeEventListener("keydown", onKey);
    document.removeEventListener("focusout", onBlur);
  });
}
