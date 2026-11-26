import { Fragment, type ReactNode } from "react";
import { Link, useMatches } from "@tanstack/react-router";
import "./breadcrumb-types";

interface Crumb {
  id: string;
  to: string;
  label: ReactNode;
}

/**
 * Builds the trail from the `breadcrumb` static data of the matched routes,
 * plus a trailing crumb for a URL-driven tab or section
 * (`breadcrumbSearch`). Parents link back; the current page is plain text.
 * Phones show only the current page.
 */
export function Breadcrumbs() {
  const matches = useMatches();
  const crumbs: Crumb[] = matches.flatMap((match) => {
    const crumb = match.staticData.breadcrumb;
    if (!crumb) return [];
    const params = match.params as Record<string, string>;
    const own: Crumb = {
      id: match.id,
      to: match.pathname,
      label: typeof crumb === "string" ? crumb : crumb(params),
    };
    const parent = match.staticData.breadcrumbParent;
    const tab = match.staticData.breadcrumbSearch?.(match.search as Record<string, unknown>);
    return [
      ...(parent ? [{ id: `${match.id}:parent`, to: parent.to, label: parent.label }] : []),
      own,
      ...(tab ? [{ id: `${match.id}:search`, to: match.pathname, label: tab }] : []),
    ];
  });

  return (
    <nav aria-label="Breadcrumb" className="min-w-0 flex-1">
      <ol className="flex min-w-0 items-center gap-1.5 text-control text-muted-foreground">
        {crumbs.map((crumb, index) => {
          if (index === crumbs.length - 1) {
            return (
              <li key={crumb.id} aria-current="page" className="min-w-0 truncate font-medium text-foreground">
                {crumb.label}
              </li>
            );
          }
          return (
            <Fragment key={crumb.id}>
              <li className="hidden shrink-0 sm:block">
                <Link
                  to={crumb.to}
                  className="rounded-xs outline-none transition-colors hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring"
                >
                  {crumb.label}
                </Link>
              </li>
              <li aria-hidden="true" className="hidden text-text-4 sm:block">
                /
              </li>
            </Fragment>
          );
        })}
      </ol>
    </nav>
  );
}
