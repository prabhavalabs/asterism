import { describe, expect, it } from "vitest";
import { MAX_ICON_BYTES, iconContentType, iconFileProblem } from "./iconUpload";

describe("icon upload checks", () => {
  it.each([
    [{ name: "logo.png", size: 10, type: "image/png" }, "image/png"],
    [{ name: "favicon.ico", size: 10, type: "image/vnd.microsoft.icon" }, "image/x-icon"],
    [{ name: "icon.SVG", size: 10, type: "" }, "image/svg+xml"],
    [{ name: "photo.jpeg", size: 10, type: "" }, "image/jpeg"],
    [{ name: "anim.gif", size: 10, type: "image/gif" }, undefined],
    [{ name: "notes.txt", size: 10, type: "text/plain" }, undefined],
  ])("derives the content type of %o", (file, want) => {
    expect(iconContentType(file)).toBe(want);
  });

  it("rejects empty, oversized and non-image files", () => {
    expect(iconFileProblem({ name: "a.png", size: 0, type: "image/png" })).toMatch(/empty/);
    expect(iconFileProblem({ name: "a.png", size: MAX_ICON_BYTES + 1, type: "image/png" })).toMatch(/512 KiB/);
    expect(iconFileProblem({ name: "a.gif", size: 10, type: "image/gif" })).toMatch(/PNG, SVG/);
    expect(iconFileProblem({ name: "a.webp", size: MAX_ICON_BYTES, type: "image/webp" })).toBeUndefined();
  });
});
