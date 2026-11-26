import { describe, expect, it } from "vitest";
import { cn } from "./utils";

describe("cn", () => {
  it.each([
    ["keeps a custom text size next to a text colour", "text-body text-foreground", "text-body text-foreground"],
    ["resolves two custom text sizes", "text-body text-control", "text-control"],
    ["keeps a custom shadow next to a ring colour", "shadow-card ring-border", "shadow-card ring-border"],
    ["resolves two custom shadows", "shadow-card shadow-pop", "shadow-pop"],
    ["treats control heights as heights", "h-8 h-control", "h-control"],
    ["resolves control-sm over control", "h-control h-control-sm", "h-control-sm"],
    ["treats pill height as a height", "h-6 h-pill", "h-pill"],
    ["resolves size tokens", "size-control size-control-sm", "size-control-sm"],
    ["resolves semantic radii", "rounded-button rounded-button-sm", "rounded-button-sm"],
    ["lets a semantic radius replace a scale radius", "rounded-none rounded-card", "rounded-card"],
    ["keeps a corner override next to a semantic radius", "rounded-card rounded-t-none", "rounded-card rounded-t-none"],
  ])("%s", (_name, input, expected) => {
    expect(cn(input)).toBe(expected);
  });
});
