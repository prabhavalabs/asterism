import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PagedSearchInput } from "./PagedSearchInput";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined;
}

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.useFakeTimers();
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

function input(): HTMLInputElement {
  const el = host.querySelector("input");
  if (!el) throw new Error("no input");
  return el;
}

/** Sets the value the way React's onChange sees a keystroke. */
function type(value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  act(() => {
    setter?.call(input(), value);
    input().dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("PagedSearchInput", () => {
  it("commits once after typing pauses", () => {
    const onCommit = vi.fn();
    act(() => root.render(<PagedSearchInput value="" onCommit={onCommit} aria-label="Search" />));
    type("r");
    type("re");
    type("red");
    expect(onCommit).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(250));
    expect(onCommit).toHaveBeenCalledTimes(1);
    expect(onCommit).toHaveBeenCalledWith("red");
  });

  it("commits immediately on Enter", () => {
    const onCommit = vi.fn();
    act(() => root.render(<PagedSearchInput value="" onCommit={onCommit} aria-label="Search" />));
    type("db");
    act(() => input().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(onCommit).toHaveBeenCalledWith("db");
    act(() => vi.advanceTimersByTime(500));
    expect(onCommit).toHaveBeenCalledTimes(1);
  });

  it("follows a query changed from outside", () => {
    const onCommit = vi.fn();
    act(() => root.render(<PagedSearchInput value="old" onCommit={onCommit} aria-label="Search" />));
    expect(input().value).toBe("old");
    act(() => root.render(<PagedSearchInput value="" onCommit={onCommit} aria-label="Search" />));
    expect(input().value).toBe("");
    expect(onCommit).not.toHaveBeenCalled();
  });
});
