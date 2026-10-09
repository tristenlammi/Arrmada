// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { RequestSheet } from "./RequestSheet";
import { api, type AuthUser, type MediaRequest } from "../lib/api";
import { ConfirmProvider, ToastProvider } from "../ui";

// REQ-04: the sheet offers each viewer exactly the actions they may take, as plain
// buttons — staff decide, owners withdraw, followers stop following — and never more.

const me: { user: AuthUser | null } = { user: null };
vi.mock("../lib/me", async (orig) => ({ ...(await orig<typeof import("../lib/me")>()), useMe: () => me }));

const at = "2026-10-01 09:00:00";
const rq = (over: Partial<MediaRequest> = {}): MediaRequest => ({
  id: 5, media_type: "movie", tmdb_id: 1, title: "Heat", year: 1995, status: "pending",
  requested_by: 7, requested_by_name: "alice", note: "the director's cut please", available: false,
  tracking: { stage: "pending" }, created_at: at, updated_at: at, ...over,
});
const user = (id: number, role: AuthUser["role"]): AuthUser => ({ id, username: `u${id}`, role, auto_approve: false, created_at: at });

function open(r: MediaRequest) {
  vi.spyOn(api, "getRequest").mockResolvedValue({ request: r, client_health: { ok: true } });
  return render(
    <MemoryRouter>
      <ToastProvider>
        <ConfirmProvider>
          <RequestSheet requestId={r.id} initial={r} onChanged={() => {}} onClose={() => {}} />
        </ConfirmProvider>
      </ToastProvider>
    </MemoryRouter>,
  );
}
const has = (name: string) => screen.queryByRole("button", { name }) !== null;

beforeEach(() => {
  vi.spyOn(api, "qualityProfiles").mockResolvedValue({
    profiles: [
      { key: "hd", name: "HD-1080p", media_type: "movie", built_in: true, is_default: true, summary: "" },
      { key: "uhd", name: "Ultra-HD 2160p", media_type: "movie", built_in: true, is_default: false, summary: "" },
    ],
    formats: [],
  });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("RequestSheet", () => {
  it("lets staff approve with a chosen profile, and shows who asked and their note", async () => {
    me.user = user(1, "admin");
    const approve = vi.spyOn(api, "approveRequest").mockResolvedValue(rq({ status: "approved" }));
    open(rq());
    expect(await screen.findByText("alice")).toBeTruthy();
    expect(screen.getByText("the director's cut please")).toBeTruthy();
    expect(has("Decline") && has("Delete")).toBe(true);
    expect(has("Withdraw") || has("Stop following")).toBe(false);
    const select = (await screen.findByRole("combobox")) as HTMLSelectElement;
    await screen.findByRole("option", { name: "Ultra-HD 2160p" });
    expect(select.value).toBe("hd"); // the default, preselected
    fireEvent.change(select, { target: { value: "uhd" } });
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    await vi.waitFor(() => expect(approve).toHaveBeenCalledWith(5, { quality_profile: "uhd" }));
  });

  it("asks before declining", async () => {
    me.user = user(1, "manager");
    const decline = vi.spyOn(api, "declineRequest").mockResolvedValue({ status: "declined" });
    open(rq());
    fireEvent.click(await screen.findByRole("button", { name: "Decline" }));
    expect(decline).not.toHaveBeenCalled();
    expect(await screen.findByRole("dialog", { name: /Decline “Heat”/ })).toBeTruthy();
  });

  it("offers the owner Withdraw on a pending request and nothing a staff member gets", async () => {
    me.user = user(7, "requester");
    open(rq({ relation: "owner" }));
    expect(await screen.findByRole("button", { name: "Withdraw" })).toBeTruthy();
    expect(has("Approve") || has("Decline") || has("Delete") || has("Stop following")).toBe(false);
  });

  it("offers a follower Stop following, and never another person's name", async () => {
    me.user = user(8, "requester");
    open(rq({ relation: "subscriber", requested_by: 0, requested_by_name: "", note: "" }));
    expect(await screen.findByRole("button", { name: "Stop following" })).toBeTruthy();
    expect(has("Withdraw") || has("Approve")).toBe(false);
    expect(screen.queryByText("alice")).toBeNull();
  });

  it("gives an owner nothing to withdraw once it's approved", async () => {
    me.user = user(7, "requester");
    open(rq({ relation: "owner", status: "approved", tracking: { stage: "searching" } }));
    await screen.findByText("Your request");
    expect(has("Withdraw")).toBe(false);
  });

  it("says what was asked for: a book's format, a show's seasons", async () => {
    me.user = user(1, "admin");
    open(rq({ media_type: "book", title: "Dune", author: "Frank Herbert", formats: "audiobook" }));
    expect(await screen.findByText("Listen")).toBeTruthy();
    cleanup();
    open(rq({ media_type: "series", title: "Silo", seasons: [1, 2, 3, 5] }));
    expect(await screen.findByText(/S1–3, S5/)).toBeTruthy();
    cleanup();
    open(rq({ media_type: "series", title: "Silo" }));
    expect(await screen.findByText(/All seasons/)).toBeTruthy();
  });
});
