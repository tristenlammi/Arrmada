import { useEffect, useMemo, useState } from "react";
import { api, type Season } from "../lib/api";
import { ReleaseSearchModal } from "./ReleaseSearchModal";

// SeriesSearchModal is the library-page "Search indexers" for a show: one modal with a
// tab for the whole show and one per season, each running the same ranked search the
// series page offers. The seasons come from the series detail, fetched on open, so the
// list page's rows don't have to carry them.
export function SeriesSearchModal({ id, title, onClose, onGrabbed }: { id: number; title: string; onClose: () => void; onGrabbed?: () => void }) {
  const [seasons, setSeasons] = useState<Season[] | null>(null);
  const [tab, setTab] = useState("all");
  useEffect(() => {
    let alive = true;
    api.seriesDetail(id).then((s) => { if (alive) setSeasons(s.seasons ?? []); }).catch(() => { if (alive) setSeasons([]); });
    return () => { alive = false; };
  }, [id]);

  const tabs = useMemo(() => {
    const t = [{ key: "all", label: "Full show" }];
    for (const s of seasons ?? []) {
      if (s.season_number === 0) continue; // specials are rarely what a pack search is for
      t.push({ key: `s${s.season_number}`, label: `Season ${s.season_number}` });
    }
    return t;
  }, [seasons]);
  const season = tab === "all" ? undefined : Number(tab.slice(1));

  return (
    <ReleaseSearchModal
      title={`Search indexers — ${title}`}
      subtitle={season ? `Season ${season}: packs and single episodes. Pick any to grab.` : "Whole-show search. Pick any pack or episode to grab."}
      tabs={tabs}
      tab={tab}
      onTab={setTab}
      fetchReleases={() => api.seriesReleases(id, season)}
      onGrab={async (rel) => { await api.grabSeries(id, { indexer: rel.indexer, download_url: rel.download_url, title: rel.title }); onGrabbed?.(); }}
      onClose={onClose}
    />
  );
}
