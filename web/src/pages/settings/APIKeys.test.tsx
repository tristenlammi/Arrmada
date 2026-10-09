// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { api, type APIKeyStatus, type AppSettings } from "../../lib/api";
import { APIKeysSection } from "./SystemSettings";

// INT-06: every testable key has a Test, a typed value can be tested before it's saved,
// and a key is tested straight after it's saved.

vi.mock("../../lib/me", () => ({ useMe: () => ({ setMetadataReady: () => {} }) }));

const key = (id: string, over: Partial<APIKeyStatus> = {}): APIKeyStatus => ({
  id, label: id.toUpperCase(), purpose: "", help_url: "", steps: "", secret: true,
  testable: true, tests_candidate: true, configured: true, source: "settings", hint: "…abcd", env_set: false, ...over,
});

beforeEach(() => {
  vi.spyOn(api, "settings").mockResolvedValue({ tmdb_region: "" } as AppSettings);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const page = () => render(<APIKeysSection onRegionSaved={() => {}} />);

describe("API keys Test", () => {
  it("shows Test for every configured testable key, none for a key that can't be tested", async () => {
    vi.spyOn(api, "apiKeys").mockResolvedValue([
      key("tmdb"), key("tvdb"), key("omdb"), key("hardcover", { tests_candidate: false }), key("opensubtitles_api"),
      key("opensubtitles_username", { testable: false, tests_candidate: false, secret: false }),
    ]);
    page();
    await screen.findByText("TMDB");
    expect(screen.getAllByRole("button", { name: "Test" })).toHaveLength(5);
  });

  it("tests a typed value without saving it, and says so", async () => {
    vi.spyOn(api, "apiKeys").mockResolvedValue([key("tmdb")]);
    const test = vi.spyOn(api, "testAPIKey").mockResolvedValue({ ok: false, detail: "TMDB rejected the key; use the v3 API key, not the v4 read access token (this looks like a v4 token)" });
    const save = vi.spyOn(api, "setAPIKey");
    page();
    await screen.findByText("TMDB");
    fireEvent.change(screen.getByPlaceholderText(/Enter a new value/), { target: { value: "eyJhbGci" } });
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Test typed" })); });
    expect(test).toHaveBeenCalledWith("tmdb", "eyJhbGci");
    expect(save).not.toHaveBeenCalled();
    expect(screen.getByText(/looks like a v4 token/)).toBeTruthy();
    expect(screen.getByText(/typed value, not saved/)).toBeTruthy();
  });

  it("offers a candidate Test before a key is set at all", async () => {
    vi.spyOn(api, "apiKeys").mockResolvedValue([key("omdb", { configured: false, source: "", hint: undefined })]);
    page();
    await screen.findByText("OMDB");
    expect(screen.queryByRole("button", { name: /Test/ })).toBeNull();
    fireEvent.change(screen.getByPlaceholderText(/Paste your key/), { target: { value: "abc" } });
    expect(screen.getByRole("button", { name: "Test typed" })).toBeTruthy();
  });

  it("tests the saved key straight after Save; the OpenSubtitles password tests through the API key", async () => {
    const keys = [key("opensubtitles_api"), key("opensubtitles_password", { testable: false, tests_candidate: false })];
    vi.spyOn(api, "apiKeys").mockResolvedValue(keys);
    vi.spyOn(api, "setAPIKey").mockResolvedValue(keys);
    const test = vi.spyOn(api, "testAPIKey").mockResolvedValue({ ok: true, detail: "OK: signed in as me, 17 downloads left today." });
    page();
    await screen.findByText("OPENSUBTITLES_PASSWORD");
    const fields = screen.getAllByPlaceholderText(/Enter a new value/);
    fireEvent.change(fields[1], { target: { value: "hunter2" } });
    await act(async () => { fireEvent.click(screen.getAllByRole("button", { name: "Save" })[1]); });
    expect(test).toHaveBeenCalledWith("opensubtitles_api", undefined);
    expect(await screen.findByText(/17 downloads left today/)).toBeTruthy();
  });
});

describe("API keys last error (INT-13)", () => {
  it("shows OMDb's last complaint under its key", async () => {
    vi.spyOn(api, "apiKeys").mockResolvedValue([
      key("omdb", { last_error: "OMDb: Request limit reached!", last_error_at: new Date(Date.now() - 5 * 60_000).toISOString() }),
      key("tmdb"),
    ]);
    page();
    expect(await screen.findByText(/Last failed 5 min ago: OMDb: Request limit reached!/)).toBeTruthy();
    expect(screen.getAllByText(/Last failed/)).toHaveLength(1);
  });
});
