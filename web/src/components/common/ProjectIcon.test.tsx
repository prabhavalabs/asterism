import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { FaviconSource, ProjectIconKind, ProjectIconSchema } from "@/gen/asterism/v1/asterism_pb";
import { ProjectIcon } from "./ProjectIcon";
import { iconSourceLabel } from "./iconSource";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined;
}

describe("ProjectIcon images", () => {
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

  it("renders the image and falls back to the monogram on error", () => {
    const icon = create(ProjectIconSchema, {
      kind: ProjectIconKind.FAVICON,
      monogram: "BL",
      colorToken: "chart-1",
      imageUrl: "/api/project-icons/abc?v=1",
      faviconSource: FaviconSource.DOMAIN,
    });
    act(() => root.render(<ProjectIcon icon={icon} name="blog" size={32} decorative={false} />));
    const tile = host.querySelector("[data-slot=icon-tile]");
    const img = host.querySelector("img");
    expect(img?.getAttribute("src")).toBe("/api/project-icons/abc?v=1");
    expect(img?.getAttribute("alt")).toBe("");
    expect(tile?.getAttribute("aria-label")).toBe("blog · favicon from the public domain");

    act(() => img?.dispatchEvent(new Event("error")));
    expect(host.querySelector("img")).toBeNull();
    expect(host.textContent).toBe("BL");
  });

  it("labels every source", () => {
    expect(iconSourceLabel(create(ProjectIconSchema, { kind: ProjectIconKind.UPLOADED }))).toBe("Uploaded image");
    expect(iconSourceLabel(create(ProjectIconSchema, { kind: ProjectIconKind.FAVICON, faviconSource: FaviconSource.DIRECTORY }))).toBe(
      "Favicon from the project directory",
    );
    expect(iconSourceLabel(undefined)).toBe("Monogram");
  });
});
