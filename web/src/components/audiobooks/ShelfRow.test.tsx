// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { AudioCard, AudioShelf } from "../../lib/api";
import { ShelfRow } from "./ShelfRow";

// Continue listening's books can be taken off it, with Undo; the other shelves can't.

const hideAudio = vi.fn(async () => {});
vi.mock("../../lib/api", () => ({ api: { hideAudio: (...a: unknown[]) => hideAudio(...(a as [])) } }));

afterEach(() => { cleanup(); hideAudio.mockClear(); });

const card = (key: string, title: string): AudioCard => ({ key, title, author: "J.K. Rowling", duration: 3600, progress: { position: 600, duration: 3600, finished: false } } as AudioCard);
const shelf = (id: string): AudioShelf => ({ id, label: id, items: [card("b1", "Rhythm of War"), card("b2", "Chamber of Secrets")] });

describe("ShelfRow", () => {
  it("removes a book from Continue listening and puts it back with Undo", async () => {
    render(<ShelfRow shelf={shelf("continue-listening")} onOpen={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Remove Rhythm of War from Continue listening" }));
    expect(hideAudio).toHaveBeenCalledWith("b1", true);
    expect(screen.queryByRole("button", { name: /^Rhythm of War/ })).toBeNull();
    expect(screen.getByRole("status").textContent).toMatch(/Removed “Rhythm of War”/);
    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    expect(hideAudio).toHaveBeenLastCalledWith("b1", false);
    await waitFor(() => expect(screen.getByRole("button", { name: /^Rhythm of War/ })).toBeTruthy());
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("offers no remove on the other shelves", () => {
    render(<ShelfRow shelf={shelf("recently-added")} onOpen={() => {}} />);
    expect(screen.queryByRole("button", { name: /^Remove/ })).toBeNull();
  });
});
