import { Link } from "react-router-dom";
import { Field, Section, Toggle, inputStyle } from "../../components/settings/ui";
import { SaveBar, useLoadedSettings } from "../../lib/useSettings";
import { LibraryFolders } from "../Library";
import { PlexLibraryUpdates } from "./PlexLibraryUpdates";
import { DEFAULT_MONITOR_PRESET, MONITOR_PRESETS, isMonitorPreset } from "../series/presets";

// Settings → Library: where each library lives on disk, and what happens when a title is
// added. Managers see this section too; LibraryFolders makes the folders read-only for them.
function SeriesMonitorDefault({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const current = isMonitorPreset(value) ? value : DEFAULT_MONITOR_PRESET;
  return (
    <Field label="Monitor">
      <select value={current} onChange={(e) => onChange(e.target.value)} className="w-full max-w-[280px] rounded-lg px-3 py-2 text-[12.5px]" style={inputStyle}>
        {MONITOR_PRESETS.map((p) => <option key={p.value} value={p.value}>{p.label}</option>)}
      </select>
      <span className="text-[10.5px] text-ink-faint">{MONITOR_PRESETS.find((p) => p.value === current)?.help}</span>
    </Field>
  );
}

export function LibrarySettings() {
  const { s, patch } = useLoadedSettings();
  return (
    <div className="flex flex-col gap-6">
      <Section id="media-folders" title="Media folders" subtitle="Where each library lives on disk.">
        <LibraryFolders />
      </Section>
      <Section id="adding-titles" title="Adding titles" subtitle="Defaults when adding movies and series.">
        <Toggle label="Search on add" hint="Start searching for a release as soon as a title is added." checked={s.search_on_add} onChange={(v) => patch({ search_on_add: v })} />
        <Link to="/quality" className="text-[12px] font-semibold" style={{ color: "var(--accent)" }}>Quality profiles →</Link>
      </Section>
      <Section id="series-monitoring" title="Series monitoring" subtitle="Which episodes a new series monitors. The add dialog starts from this, and requests use it.">
        <SeriesMonitorDefault value={s.series_monitor_default} onChange={(v) => patch({ series_monitor_default: v })} />
      </Section>
      <SaveBar />
      {/* Has its own Save (it isn't part of the settings draft above). Moves to the Plex
          settings page when that lands. */}
      <PlexLibraryUpdates />
    </div>
  );
}
