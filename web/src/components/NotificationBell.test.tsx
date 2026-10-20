// @vitest-environment jsdom
import { useEffect } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { api, type UserNotification } from "../lib/api";
import { NotificationBell, posterOf } from "./NotificationBell";

// The bell loads once on mount here; the live feed and fallback poll are not under test.
vi.mock("../lib/useLiveRefresh", () => ({
  useLiveRefresh: (fn: () => void) => { useEffect(() => { fn(); }, []); return () => {}; },
}));

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const note = (over: Partial<UserNotification>): UserNotification => ({
  id: 1, title: "Your request is ready", body: "“Dune” is ready to watch.", media_type: "movie", ref: "movie:438631", read: false, created_at: 1, ...over,
});

describe("NotificationBell posters", () => {
  it("shows a ready title's poster thumbnail, and text alone for an old notice", async () => {
    vi.spyOn(api, "myNotifications").mockResolvedValue({
      unread: 2,
      notifications: [
        note({ id: 2, poster_url: "https://image.tmdb.org/t/p/w500/dune.jpg" }),
        note({ id: 1, title: "Request approved", body: "Your request for “Arrival” was approved.", ref: "movie:329865" }),
      ],
    });
    const { container } = render(<MemoryRouter><NotificationBell /></MemoryRouter>);
    fireEvent.click(await screen.findByLabelText("Notifications"));
    await waitFor(() => expect(screen.getByText("Request approved")).toBeTruthy());
    const imgs = container.querySelectorAll("img");
    expect(imgs.length).toBe(1);
    expect(imgs[0].getAttribute("src")).toBe("https://image.tmdb.org/t/p/w342/dune.jpg");
    expect(screen.getByText("“Dune” is ready to watch.")).toBeTruthy();
  });

  it("only draws posters from TMDB's image host", () => {
    expect(posterOf({ poster_url: "https://image.tmdb.org/t/p/w500/a.jpg" })).toBe("https://image.tmdb.org/t/p/w342/a.jpg");
    for (const bad of [undefined, "", "https://evil.example.com/a.jpg", "/api/v1/movies/1/poster", "javascript:alert(1)"]) {
      expect(posterOf({ poster_url: bad })).toBeUndefined();
    }
  });
});
