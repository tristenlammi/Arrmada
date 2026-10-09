import type { ReactNode } from "react";
import { Navigate, type RouteObject } from "react-router-dom";
import type { UserRole } from "./api";
import type { RouteHandle } from "./title";
import { AppLayout } from "../components/AppLayout";
import { UserLayout } from "../components/UserLayout";
import { RouteError } from "../components/RouteError";
import { ModuleGate } from "../components/ModuleGate";
import { MyBooks } from "../pages/MyBooks";
import { Dashboard } from "../pages/Dashboard";
import { Quality } from "../pages/Quality";
import { Indexers } from "../pages/Indexers";
import { DownloadClients } from "../pages/DownloadClients";
import { Settings } from "../pages/Settings";
import { Downloads } from "../pages/Downloads";
import { History } from "../pages/History";
import { Reviews } from "../pages/Reviews";
import { Movies } from "../pages/Movies";
import { MovieDetail } from "../pages/MovieDetail";
import { Series } from "../pages/Series";
import { SeriesDetail } from "../pages/SeriesDetail";
import { Discover } from "../pages/Discover";
import { Books } from "../pages/Books";
import { Music } from "../pages/Music";
import { ArtistDetail } from "../pages/ArtistDetail";
import { AlbumDetail } from "../pages/AlbumDetail";
import { BookDetail } from "../pages/BookDetail";
import { AuthorDetail } from "../pages/AuthorDetail";
import { Subtitles } from "../pages/Subtitles";
import { Convert } from "../pages/Convert";
import { Insights } from "../pages/Insights";
import { Audiobooks } from "../pages/Audiobooks";
import { Calendar } from "../pages/Calendar";
import { Logs } from "../pages/Logs";
import { NotFound } from "../pages/NotFound";

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

function requesterRoutes(shell: "external" | "requester"): RouteObject[] {
  const home = "/discover";
  return [
    page("/discover", "Discover", <Discover chrome={false} />),
    // Outside sessions get no Calendar: the API isn't allowlisted for them.
    ...(shell === "requester" ? [page("/calendar", "Calendar", <Calendar chrome={false} />)] : []),
    page("/books", "Books", <ModuleGate module="books" home={home}><MyBooks /></ModuleGate>),
    page("/audiobooks", "Audiobooks", <Audiobooks chrome={false} />),
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
    page("/movies", "Movies", <Movies />),
    page("/movies/:id", "Movie", <MovieDetail />),
    page("/series", "Series", <Series />),
    page("/series/:id", "Series", <SeriesDetail />),
    page("/discover", "Discover", <Discover />),
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
    page("/indexers", "Indexers", <Indexers />),
    page("/downloadclients", "Download clients", <DownloadClients />),
    redirect("/notifications", "/insights"),
    // A splat (which also matches plain /settings), so Settings can give its sections
    // their own addresses.
    page("/settings/*", "Settings", <Settings />),
    page("/quality", "Quality profiles", <Quality />),
    // The log is admin-only on the server; for a manager the address falls through to Not found.
    ...(admin ? [page("/logs", "Logs", <Logs />)] : []),
    redirect("/library", "/settings"),
    page("*", "Page not found", <NotFound />),
  ];
}

// buildRoutes returns the route table for one signed-in session. The router is rebuilt
// only when role or external changes, which happens at sign-in and sign-out (both full
// loads anyway). Module toggles don't rebuild it; ModuleGate handles those live.
export function buildRoutes({ role, external }: { role: UserRole; external: boolean }): RouteObject[] {
  const shell = shellFor(role, external);
  if (shell === "staff") {
    return [{ element: <AppLayout />, errorElement: <RouteError />, children: staffRoutes(role === "admin") }];
  }
  return [{ element: <UserLayout />, errorElement: <RouteError />, children: requesterRoutes(shell) }];
}
