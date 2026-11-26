import { useEffect, useRef, useState } from "react";
import { SearchInput, type SearchInputProps } from "./SearchInput";

export interface PagedSearchInputProps extends Omit<SearchInputProps, "value" | "onValueChange" | "defaultValue"> {
  /** The list's committed query (`paged.query`). */
  value: string;
  /** Commits the query (`paged.setQuery`); called after typing pauses. */
  onCommit: (query: string) => void;
  /** Pause before a keystroke becomes a request (default 250 ms). */
  delayMs?: number;
}

/**
 * SearchInput for a server-side paginated list. Typing updates the field at
 * once and commits to the URL (and so to the request) only after a short
 * pause, so a word is one request, not one per letter. Enter commits
 * immediately. A query changed elsewhere (back/forward, "Clear filters")
 * replaces the field's text.
 */
export function PagedSearchInput({ value, onCommit, delayMs = 250, onKeyDown, ...props }: PagedSearchInputProps) {
  const [draft, setDraft] = useState(value);
  const committed = useRef(value);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Follow outside changes of the committed query.
  useEffect(() => {
    if (value !== committed.current) {
      committed.current = value;
      setDraft(value);
    }
  }, [value]);

  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );

  const commit = (next: string) => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = null;
    if (next === committed.current) return;
    committed.current = next;
    onCommit(next);
  };

  return (
    <SearchInput
      {...props}
      value={draft}
      onValueChange={(next) => {
        setDraft(next);
        if (timer.current) clearTimeout(timer.current);
        timer.current = setTimeout(() => commit(next), next === "" ? 0 : delayMs);
      }}
      onKeyDown={(event) => {
        onKeyDown?.(event);
        if (event.key === "Enter") commit(draft);
      }}
    />
  );
}
