// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { ServerBadge } from "./ServerBadge";
import { SERVER_LABEL, jellyfinSlotIsEmby, setJellyfinSlotKind, watchLabel } from "./servers";

// An Emby server in the Jellyfin slot is named and drawn as Emby wherever the slot shows.
describe("the Jellyfin slot holding Emby", () => {
  afterEach(() => { cleanup(); setJellyfinSlotKind(undefined); });

  it("relabels and recolours the slot, and goes back", () => {
    setJellyfinSlotKind("emby");
    expect(jellyfinSlotIsEmby()).toBe(true);
    expect(SERVER_LABEL.jellyfin).toBe("Emby");
    expect(watchLabel({ server: "jellyfin", label: "Emby", url: "https://e" })).toBe("Emby");
    render(<ServerBadge server="jellyfin" />);
    const badge = screen.getByText("Emby");
    expect(badge.style.background).toContain("var(--emby)");
    expect(badge.style.color).toBe("var(--emby-ink)");

    setJellyfinSlotKind("jellyfin");
    expect(SERVER_LABEL.jellyfin).toBe("Jellyfin");
    expect(SERVER_LABEL.plex).toBe("Plex");
    setJellyfinSlotKind(undefined);
    expect(jellyfinSlotIsEmby()).toBe(false);
  });
});
