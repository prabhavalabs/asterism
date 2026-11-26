/** Largest icon the hub accepts (SetProjectIcon). */
export const MAX_ICON_BYTES = 512 * 1024;

/** File types the upload control offers; the hub sniffs the real type. */
export const ICON_ACCEPT = ".png,.svg,.ico,.webp,.jpg,.jpeg,image/png,image/svg+xml,image/x-icon,image/vnd.microsoft.icon,image/webp,image/jpeg";

const EXTENSION_TYPES: Record<string, string> = {
  png: "image/png",
  svg: "image/svg+xml",
  ico: "image/x-icon",
  webp: "image/webp",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
};

const ALLOWED_TYPES = new Set([...Object.values(EXTENSION_TYPES), "image/vnd.microsoft.icon"]);

export interface IconFileLike {
  name: string;
  size: number;
  type: string;
}

/** The content type to declare for a file, from its MIME type or extension. */
export function iconContentType(file: IconFileLike): string | undefined {
  const type = file.type.toLowerCase();
  if (ALLOWED_TYPES.has(type)) return type === "image/vnd.microsoft.icon" ? "image/x-icon" : type;
  const ext = file.name.toLowerCase().split(".").pop() ?? "";
  return EXTENSION_TYPES[ext];
}

/** A reason the file cannot be uploaded, or undefined when it looks fine. */
export function iconFileProblem(file: IconFileLike): string | undefined {
  if (!iconContentType(file)) return "Choose a PNG, SVG, ICO, WebP or JPEG image.";
  if (file.size === 0) return "That file is empty.";
  if (file.size > MAX_ICON_BYTES) return `That file is ${Math.ceil(file.size / 1024)} KiB; icons can be at most 512 KiB.`;
  return undefined;
}
