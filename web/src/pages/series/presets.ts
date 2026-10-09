// Monitoring presets: the common ways to say which episodes of a show are wanted. The
// server applies them (series.ApplyMonitorPreset); these are the names and the help text.
export type MonitorPreset = "all" | "future" | "missing" | "existing" | "first_season" | "latest_season" | "none";

export const MONITOR_PRESETS: readonly { value: MonitorPreset; label: string; help: string }[] = [
  { value: "all", label: "All episodes", help: "Every episode, and new seasons as they come." },
  { value: "future", label: "Future episodes", help: "Only episodes that haven't aired yet — nothing already out is grabbed." },
  { value: "missing", label: "Missing episodes", help: "Episodes without a file, and ones still to air. Episodes with files won't be upgraded." },
  { value: "existing", label: "Existing episodes", help: "Episodes you already have (kept upgraded), and ones still to air." },
  { value: "first_season", label: "First season", help: "Season 1 only. New seasons aren't monitored." },
  { value: "latest_season", label: "Latest season", help: "The newest season, and new seasons as they come." },
  { value: "none", label: "None", help: "Nothing is monitored." },
];

export const DEFAULT_MONITOR_PRESET: MonitorPreset = "all";

export function isMonitorPreset(v: unknown): v is MonitorPreset {
  return MONITOR_PRESETS.some((p) => p.value === v);
}
