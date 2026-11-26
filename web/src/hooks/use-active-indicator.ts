import { useLayoutEffect, type RefObject } from "react";

/**
 * Slides one indicator element under the active item of a list in 220ms on
 * the ease-out curve. Keyboard changes, the first placement and resizes jump
 * without animating. Styles are written straight to the indicator so no
 * React render happens per change.
 *
 * `activeSelector` finds the active item inside the list, for example
 * `[role="tab"][data-state="active"]` or `[role="radio"][aria-checked="true"]`.
 */
export function useActiveIndicator(
  listRef: RefObject<HTMLElement | null>,
  indicatorRef: RefObject<HTMLElement | null>,
  activeSelector: string,
  observedAttribute: string,
  enabled = true,
): void {
  useLayoutEffect(() => {
    const list = listRef.current;
    const indicator = indicatorRef.current;
    if (!enabled || !list || !indicator) return;

    let fromKeyboard = false;

    const place = (animate: boolean) => {
      const active = list.querySelector<HTMLElement>(activeSelector);
      if (!active) {
        delete indicator.dataset.ready;
        return;
      }
      if (!animate) indicator.style.transition = "none";
      indicator.style.width = `${active.offsetWidth}px`;
      indicator.style.height = `${active.offsetHeight}px`;
      indicator.style.transform = `translate(${active.offsetLeft}px, ${active.offsetTop}px)`;
      indicator.dataset.ready = "";
      if (!animate) {
        // Flush the jump while transitions are off, then restore them.
        void indicator.offsetWidth;
        indicator.style.transition = "";
      }
    };

    const onKeyDown = () => {
      fromKeyboard = true;
    };
    const onPointerDown = () => {
      fromKeyboard = false;
    };

    place(false);
    list.addEventListener("keydown", onKeyDown);
    list.addEventListener("pointerdown", onPointerDown);

    const mutations = new MutationObserver(() => place(!fromKeyboard));
    mutations.observe(list, {
      subtree: true,
      childList: true,
      attributes: true,
      attributeFilter: [observedAttribute],
    });

    const resizes =
      typeof ResizeObserver === "undefined" ? null : new ResizeObserver(() => place(false));
    resizes?.observe(list);

    return () => {
      list.removeEventListener("keydown", onKeyDown);
      list.removeEventListener("pointerdown", onPointerDown);
      mutations.disconnect();
      resizes?.disconnect();
    };
  }, [listRef, indicatorRef, activeSelector, observedAttribute, enabled]);
}
