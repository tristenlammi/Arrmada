// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { Backups } from "./Backups";
import { systemApi } from "../../lib/systemApi";
import type { BackupsState } from "../../lib/api";

vi.mock("../../lib/systemApi", () => ({ systemApi: { backups: vi.fn() } }));
afterEach(() => { cleanup(); vi.clearAllMocks(); });

function state(backend: "sqlite" | "postgres"): BackupsState {
  return {
    backend, can_restore: backend === "sqlite", can_upload: backend === "sqlite",
    backups: [{ backend, name: `arrmada-manual-20261020T000000Z.${backend === "sqlite" ? "db" : "pgdump"}`, kind: "manual", size_bytes: 1000, created_at: "2026-10-20T00:00:00Z", schema_version: "0004_subtitle_job_ids" }],
    total_bytes: 1000, free_bytes: 100000, dir: "/data/backups", settings: { enabled: true, hour: 4, keep_nightly: 7 },
    last_nightly_at: null, can_restart: false, pending_restore: null, last_restore: null,
  };
}

it("offers native PostgreSQL downloads and offline recovery, not in-app restore/upload", async () => {
  vi.mocked(systemApi.backups).mockResolvedValue(state("postgres"));
  render(<Backups />);
  expect(await screen.findByRole("link", { name: "Download" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Restore" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Restore from file…" })).toBeNull();
  expect(screen.getByText(/arrmada restore-postgres/)).toBeTruthy();
  expect(screen.getByRole("button", { name: "Back up now" })).toBeTruthy();
});

it("preserves SQLite restore and upload controls", async () => {
  vi.mocked(systemApi.backups).mockResolvedValue(state("sqlite"));
  render(<Backups />);
  expect(await screen.findByRole("button", { name: "Restore" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Restore from file…" })).toBeTruthy();
  expect(screen.queryByText(/arrmada restore-postgres/)).toBeNull();
});
