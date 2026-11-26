/**
 * Stable, module-level chart props. UPlotChart rebuilds its plot when these
 * change identity, so never pass inline lambdas or array literals.
 */
export const RATIO_RANGE: [number, number] = [0, 1];

/** Axis/legend label for a 0..1 ratio: `42%`. */
export function formatRatioAxis(value: number): string {
  return `${Math.round(value * 100)}%`;
}
