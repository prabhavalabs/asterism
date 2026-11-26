/** Sentence-case label for a project category ("production" → "Production"). */
export function categoryLabel(category: string): string {
  const trimmed = category.trim();
  if (!trimmed) return "Uncategorised";
  const spaced = trimmed.replace(/[-_]+/g, " ");
  return spaced.charAt(0).toUpperCase() + spaced.slice(1).toLowerCase();
}

/** Project name from an entity ID such as `proj:analytics-hub`. */
export function projectNameFromId(projectId: string): string {
  return projectId.startsWith("proj:") ? projectId.slice("proj:".length) : projectId;
}
