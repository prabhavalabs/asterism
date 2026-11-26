import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DataPagination } from "./DataPagination";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined;
}

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

function button(name: string): HTMLButtonElement {
  const found = [...host.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === name);
  if (!found) throw new Error(`no button ${name}`);
  return found;
}

describe("DataPagination", () => {
  it("renders the summary, numbered pages with ellipses and the current page", () => {
    act(() =>
      root.render(<DataPagination page={2} pageSize={25} totalSize={137} noun="volumes" onPageChange={() => {}} />),
    );
    expect(host.querySelector("p")?.textContent).toBe("Showing 26–50 of 137 volumes");
    const pages = [...host.querySelectorAll("[data-slot=pagination-link]")].map((b) => b.textContent);
    expect(pages).toEqual(["1", "2", "3", "4", "5", "6"]);
    expect(host.querySelector("[aria-current=page]")?.textContent).toBe("2");
  });

  it("collapses long ranges with ellipses", () => {
    act(() => root.render(<DataPagination page={10} pageSize={10} totalSize={500} onPageChange={() => {}} />));
    const items = [...host.querySelectorAll("[data-slot=pagination-link], [data-slot=pagination-ellipsis]")].map(
      (el) => el.textContent || "…",
    );
    expect(items).toEqual(["1", "…", "9", "10", "11", "…", "50"]);
  });

  it("steps pages and disables the ends", () => {
    const onPageChange = vi.fn();
    act(() => root.render(<DataPagination page={1} pageSize={10} totalSize={35} onPageChange={onPageChange} />));
    expect(button("First page").disabled).toBe(true);
    expect(button("Previous page").disabled).toBe(true);
    act(() => button("Next page").click());
    act(() => button("Last page").click());
    act(() => button("Page 3").click());
    expect(onPageChange.mock.calls).toEqual([[2], [4], [3]]);

    act(() => root.render(<DataPagination page={4} pageSize={10} totalSize={35} onPageChange={onPageChange} />));
    expect(button("Next page").disabled).toBe(true);
    expect(button("Last page").disabled).toBe(true);
  });

  it("shows the rows-per-page select only with a handler", () => {
    act(() => root.render(<DataPagination page={1} pageSize={25} totalSize={40} onPageChange={() => {}} />));
    expect(host.querySelector("[data-slot=select-trigger]")).toBeNull();
    act(() =>
      root.render(
        <DataPagination page={1} pageSize={25} totalSize={40} onPageChange={() => {}} onPageSizeChange={() => {}} />,
      ),
    );
    expect(host.querySelector("[data-slot=select-trigger]")?.textContent).toContain("25");
  });
});
