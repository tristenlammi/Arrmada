import { useEffect, useState } from "react";
import { isRouteErrorResponse, useRouteError } from "react-router-dom";
import { ErrorCard } from "./ErrorBoundary";
import { reloadForChunkError } from "../lib/chunkReload";

// RouteError is every route's errorElement: an exception while rendering a page shows the
// same card as the layout's ErrorBoundary, inside the shell, and the router clears it when
// you navigate away. A page chunk that went missing in a deploy reloads once first.
export function RouteError() {
  const raw = useRouteError();
  const [reloading, setReloading] = useState(false);
  useEffect(() => {
    if (reloadForChunkError(raw)) setReloading(true);
  }, [raw]);
  if (reloading) return null;
  return <ErrorCard error={toError(raw)} />;
}

export function toError(raw: unknown): Error {
  if (raw instanceof Error) return raw;
  if (isRouteErrorResponse(raw)) return new Error(`${raw.status} ${raw.statusText}`.trim());
  return new Error(String(raw));
}
