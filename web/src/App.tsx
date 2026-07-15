import { Routes, Route, Navigate } from "react-router-dom";
import { AppLayout } from "./components/AppLayout";
import { UserLayout } from "./components/UserLayout";
import { useMe, isStaff } from "./lib/me";
import { Dashboard } from "./pages/Dashboard";
import { Quality } from "./pages/Quality";
import { Indexers } from "./pages/Indexers";
import { DownloadClients } from "./pages/DownloadClients";
import { Settings } from "./pages/Settings";
import { Downloads } from "./pages/Downloads";
import { History } from "./pages/History";
import { Reviews } from "./pages/Reviews";
import { Movies } from "./pages/Movies";
import { MovieDetail } from "./pages/MovieDetail";
import { Series } from "./pages/Series";
import { SeriesDetail } from "./pages/SeriesDetail";
import { Requests } from "./pages/Requests";
import { Discover } from "./pages/Discover";
import { Books } from "./pages/Books";
import { BookDetail } from "./pages/BookDetail";
import { AuthorDetail } from "./pages/AuthorDetail";
import { Subtitles } from "./pages/Subtitles";
import { Convert } from "./pages/Convert";
import { Insights } from "./pages/Insights";
import { Calendar } from "./pages/Calendar";
import { Library } from "./pages/Library";
import { Login } from "./pages/Login";
import { Placeholder } from "./pages/Placeholder";

// Module routes still awaiting their build → placeholders. Milestones mirror
// BUILD-PLAN.md.
const MODULE_ROUTES: {
  path: string;
  title: string;
  crumb: string;
  milestone: string;
}[] = [
  { path: "/music", title: "Music", crumb: "Library / Music", milestone: "M8" },
];

export default function App() {
  const { user, loading } = useMe();

  if (loading) {
    return <div className="grid h-full place-items-center text-[13px] text-ink-dim">Loading…</div>;
  }

  // Auth enabled + not signed in → login / first-run setup.
  if (!user) {
    return <Login />;
  }

  // Non-staff (requesters/readonly) only ever get the Discover experience — no nav.
  if (!isStaff(user)) {
    return (
      <Routes>
        <Route element={<UserLayout />}>
          <Route path="/discover" element={<Discover chrome={false} />} />
          <Route path="/calendar" element={<Calendar chrome={false} />} />
          <Route path="*" element={<Navigate to="/discover" replace />} />
        </Route>
      </Routes>
    );
  }

  return (
    <Routes>
      <Route element={<AppLayout />}>
        <Route index element={<Dashboard />} />
        <Route path="/downloads" element={<Downloads />} />
        <Route path="/activity" element={<Navigate to="/downloads" replace />} />
        <Route path="/history" element={<History />} />
        <Route path="/review" element={<Reviews />} />
        <Route path="/movies" element={<Movies />} />
        <Route path="/movies/:id" element={<MovieDetail />} />
        <Route path="/series" element={<Series />} />
        <Route path="/series/:id" element={<SeriesDetail />} />
        <Route path="/discover" element={<Discover />} />
        <Route path="/calendar" element={<Calendar />} />
        <Route path="/books" element={<Books />} />
        <Route path="/books/author/:name" element={<AuthorDetail />} />
        <Route path="/books/:id" element={<BookDetail />} />
        <Route path="/requests" element={<Requests />} />
        <Route path="/subtitles" element={<Subtitles />} />
        <Route path="/convert" element={<Convert />} />
        <Route path="/insights" element={<Insights />} />
        <Route path="/indexers" element={<Indexers />} />
        <Route path="/downloadclients" element={<DownloadClients />} />
        <Route path="/notifications" element={<Navigate to="/insights" replace />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/quality" element={<Quality />} />
        <Route path="/library" element={<Library />} />
        {MODULE_ROUTES.map((m) => (
          <Route
            key={m.path}
            path={m.path}
            element={
              <Placeholder title={m.title} crumb={m.crumb} milestone={m.milestone} />
            }
          />
        ))}
        <Route
          path="*"
          element={<Placeholder title="Not found" note="No such page." />}
        />
      </Route>
    </Routes>
  );
}
