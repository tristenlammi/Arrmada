import { lazy, type ComponentType, type LazyExoticComponent } from "react";

// lazyPage turns a page module's named export into a React.lazy component, so each page
// is its own chunk and is only downloaded when someone opens it. Pages use named exports
// (export function Movies), which React.lazy can't take directly.
//
//   const Movies = lazyPage(() => import("../pages/Movies"), "Movies");
//
// A chunk that fails to load (usually a deploy replaced it) throws into the route's error
// element, which reloads once to pick up the new build.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function lazyPage<M extends Record<K, ComponentType<any>>, K extends keyof M & string>(
  load: () => Promise<M>,
  name: K,
): LazyExoticComponent<M[K]> {
  return lazy(() => load().then((m) => ({ default: m[name] })));
}
