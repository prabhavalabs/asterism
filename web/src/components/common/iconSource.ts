import { FaviconSource, ProjectIconKind, type ProjectIcon } from "@/gen/asterism/v1/asterism_pb";

/** Short description of where a project icon came from. */
export function iconSourceLabel(icon: ProjectIcon | undefined): string {
  switch (icon?.kind) {
    case ProjectIconKind.UPLOADED:
      return "Uploaded image";
    case ProjectIconKind.FAVICON:
      switch (icon.faviconSource) {
        case FaviconSource.DOMAIN:
          return "Favicon from the public domain";
        case FaviconSource.GENERATED:
          return "Generated favicon (showcase)";
        default:
          return "Favicon from the project directory";
      }
    case ProjectIconKind.CONFIGURED:
      return "Set in asterism.toml";
    case ProjectIconKind.LABEL:
      return "From the asterism.icon label";
    case ProjectIconKind.DETECTED:
      return "Detected from the project's images";
    default:
      return "Monogram";
  }
}
