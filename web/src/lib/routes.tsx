import { Suspense, type ReactNode } from "react";
import { Navigate, useLocation, useParams, type RouteObject } from "react-router-dom";
import type { UserRole } from "./api";
import type { RouteHandle } from "./title";
import { UserLayout } from "../components/UserLayout";
import { RouteError } from "../components/RouteError";
import { ModuleGate } from "../components/ModuleGate";
import { lazyPage } from "./lazyPage";
import { LINKS } from "./links";

// The staff console's frame (sidebar, Needs-you polling, restart banner) is its own
// chunk too: a requester's phone loads only the small requester shell with Discover.
const AppLayout = lazyPage(() => import("../components/AppLayout"), "AppLayout");
const staffShellLoading = <div className="grid h-full place-items-center text-[13px] text-ink-dim">Loading…</div>;

// Every page is its own chunk (FE-06): a requester downloads Discover and their few
// pages, never Quality, Convert, Settings or the rest of the console.
const MyBooks = lazyPage(() => import("../pages/MyBooks"), "MyBooks");
const Dashboard = lazyPage(() => import("../pages/Dashboard"), "Dashboard");
const Quality = lazyPage(() => import("../pages/Quality"), "Quality");
const Indexers = lazyPage(() => import("../pages/Indexers"), "Indexers");
const DownloadClients = lazyPage(() => import("../pages/DownloadClients"), "DownloadClients");
const Settings = lazyPage(() => import("../pages/Settings"), "Settings");
const Downloads = lazyPage(() => import("../pages/Downloads"), "Downloads");
const History = lazyPage(() => import("../pages/History"), "History");
const Reviews = lazyPage(() => import("../pages/Reviews"), "Reviews");
const Blocklist = lazyPage(() => import("../pages/Blocklist"), "Blocklist");
const Movies = lazyPage(() => import("../pages/Movies"), "Movies");
const MovieDetail = lazyPage(() => import("../pages/MovieDetail"), "MovieDetail");
const MoviesWanted = lazyPage(() => import("../pages/MoviesWanted"), "MoviesWanted");
const Series = lazyPage(() => import("../pages/Series"), "Series");
const SeriesDetail = lazyPage(() => import("../pages/SeriesDetail"), "SeriesDetail");
const Discover = lazyPage(() => import("../pages/Discover"), "Discover");
const Requests = lazyPage(() => import("../pages/Requests"), "Requests");
const Books = lazyPage(() => import("../pages/Books"), "Books");
const Music = lazyPage(() => import("../pages/Music"), "Music");
const ArtistDetail = lazyPage(() => import("../pages/ArtistDetail"), "ArtistDetail");
const AlbumDetail = lazyPage(() => import("../pages/AlbumDetail"), "AlbumDetail");
const BookDetail = lazyPage(() => import("../pages/BookDetail"), "BookDetail");
const AuthorDetail = lazyPage(() => import("../pages/AuthorDetail"), "AuthorDetail");
const Subtitles = lazyPage(() => import("../pages/Subtitles"), "Subtitles");
const Convert = lazyPage(() => import("../pages/Convert"), "Convert");
const Insights = lazyPage(() => import("../pages/Insights"), "Insights");
const Audiobooks = lazyPage(() => import("../pages/Audiobooks"), "Audiobooks");
const Calendar = lazyPage(() => import("../pages/Calendar"), "Calendar");
const Logs = lazyPage(() => import("../pages/Logs"), "Logs");
const Me = lazyPage(() => import("../pages/Me"), "Me");
const NotFound = lazyPage(() => import("../pages/NotFound"), "NotFound");

// Three shells, one table each. Staff get the whole console; requesters and outside
// visitors get the small requester shell. The server enforces the same split, so these
// tables only decide what's worth showing.
export type Shell = "external" | "requester" | "staff";

export function shellFor(role: UserRole, external: boolean): Shell {
  if (role === "admin" || role === "manager") return "staff";
  // "external" is the server's verdict that this session is from outside the LAN and
  // not staff; staff get the whole app wherever they sign in from.
  return external ? "external" : "requester";
}

// page is one route: its tab title, and the error card shown inside the layout if it throws.
function page(path: string, title: string, element: ReactNode): RouteObject {
  const handle: RouteHandle = { title };
  return { path, element, handle, errorElement: <RouteError /> };
}

function redirect(path: string, to: string): RouteObject {
  return { path, element: <Navigate to={to} replace />, errorElement: <RouteError /> };
}

// discover is Discover plus its titles' own addresses (/discover/movie/603,
// /discover/series/1399), in every shell. A title is a child of /discover so the page
// stays mounted under the title's sheet (Discover reads the match and opens it), keeping
// its rows and scroll position. TMDB's own "tv" spelling redirects to "series".
function discover(element: ReactNode): RouteObject {
  const title: RouteHandle = { title: "Discover" };
  return {
    path: "/discover",
    element,
    handle: title,
    errorElement: <RouteError />,
    children: [
      { path: "movie/:tmdbId", element: null, handle: title, errorElement: <RouteError /> },
      { path: "series/:tmdbId", element: null, handle: title, errorElement: <RouteError /> },
      { path: "tv/:tmdbId", element: <TvRedirect />, handle: title, errorElement: <RouteError /> },
    ],
  };
}

function TvRedirect() {
  const { tmdbId } = useParams();
  const { search } = useLocation();
  return <Navigate to={`/discover/series/${tmdbId ?? ""}${search}`} replace />;
}

function requesterRoutes(shell: "external" | "requester"): RouteObject[] {
  const home = "/discover";
  return [
    discover(<Discover chrome={false} />),
    // Their own requests and the ones they follow (/api/v1/requests is allowed from outside).
    page("/requests", "Requests", <Requests chrome={false} />),
    // Outside sessions get no Calendar: the API isn't allowlisted for them.
    ...(shell === "requester" ? [page("/calendar", "Calendar", <Calendar chrome={false} />)] : []),
    // "My shelf": the books they can download. /books was its old address, kept so
    // installed shortcuts and bookmarks still land; Discover's Books tab is the catalogue.
    page("/shelf", "My shelf", <ModuleGate module="books" home={home}><MyBooks /></ModuleGate>),
    redirect("/books", "/shelf"),
    page("/audiobooks", "Audiobooks", <Audiobooks chrome={false} />),
    page("/me", "Me", <Me />),
    redirect("*", home),
  ];
}

function staffRoutes(admin: boolean): RouteObject[] {
  const home = "/";
  const books = (el: ReactNode) => <ModuleGate module="books" home={home}>{el}</ModuleGate>;
  const music = (el: ReactNode) => <ModuleGate module="music" home={home}>{el}</ModuleGate>;
  return [
    { index: true, element: <Dashboard />, handle: { title: "Dashboard" } satisfies RouteHandle, errorElement: <RouteError /> },
    page("/downloads", "Downloads", <Downloads />),
    redirect("/activity", "/downloads"),
    page("/history", "History", <History />),
    page("/review", "Review", <Reviews />),
    page("/blocklist", "Blocklist", <Blocklist />),
    page("/movies", "Movies", <Movies />),
    page("/movies/wanted", "Wanted movies", <MoviesWanted />),
    page("/movies/:id", "Movie", <MovieDetail />),
    page("/series", "Series", <Series />),
    page("/series/:id", "Series", <SeriesDetail />),
    discover(<Discover />),
    page("/requests", "Requests", <Requests />),
    page("/calendar", "Calendar", <Calendar />),
    page("/music", "Music", music(<Music />)),
    page("/music/album/:id", "Album", music(<AlbumDetail />)),
    page("/music/:id", "Artist", music(<ArtistDetail />)),
    page("/books", "Books", books(<Books />)),
    page("/books/author/:name", "Author", books(<AuthorDetail />)),
    page("/books/:id", "Book", books(<BookDetail />)),
    page("/subtitles", "Subtitles", <Subtitles />),
    page("/convert", "Convert", <Convert />),
    page("/insights", "Insights", <Insights />),
    page("/audiobooks", "Audiobooks", <Audiobooks />),
    page("/me", "Me", <Me chrome />),
    page("/indexers", "Indexers", <Indexers />),
    page("/downloadclients", "Download clients", <DownloadClients />),
    // Alerts moved out of Insights into Settings; both old addresses land there.
    redirect("/notifications", LINKS.alerts),
    redirect("/alerts", LINKS.alerts),
    // A splat (which also matches plain /settings), so Settings can give its sections
    // their own addresses.
    page("/settings/*", "Settings", <Settings />),
    page("/quality", "Quality profiles", <Quality />),
    // The log is admin-only on the server; for a manager the address falls through to Not found.
    ...(admin ? [page("/logs", "Logs", <Logs />)] : []),
    redirect("/library", "/settings/library"),
    page("*", "Page not found", <NotFound />),
  ];
}

// buildRoutes returns the route table for one signed-in session. The router is rebuilt
// only when role or external changes, which happens at sign-in and sign-out (both full
// loads anyway). Module toggles don't rebuild it; ModuleGate handles those live.
export function buildRoutes({ role, external }: { role: UserRole; external: boolean }): RouteObject[] {
  const shell = shellFor(role, external);
  if (shell === "staff") {
    const frame = <Suspense fallback={staffShellLoading}><AppLayout /></Suspense>;
    return [{ element: frame, errorElement: <RouteError />, children: staffRoutes(role === "admin") }];
  }
  return [{ element: <UserLayout />, errorElement: <RouteError />, children: requesterRoutes(shell) }];
}
