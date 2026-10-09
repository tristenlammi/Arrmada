import { Link } from "react-router-dom";
import { Section, Toggle } from "../../components/settings/ui";
import { SaveBar, useLoadedSettings } from "../../lib/useSettings";
import { LibraryFolders } from "../Library";

// Settings → Library: where each library lives on disk, and what happens when a title is
// added. Managers see this section too; LibraryFolders makes the folders read-only for them.
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
      <SaveBar />
    </div>
  );
}
