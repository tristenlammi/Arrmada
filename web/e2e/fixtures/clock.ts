// The browser's clock is pinned to this instant (see mockApi.ts), so date-driven
// pages (Calendar, "added 3 days ago") render the same on every run.
export const NOW = Date.parse("2026-10-09T12:00:00Z");

// day returns YYYY-MM-DD for NOW plus `offset` days.
export function day(offset: number): string {
  return new Date(NOW + offset * 86_400_000).toISOString().slice(0, 10);
}
