// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { ConfirmProvider, useConfirm, type ConfirmFn } from "./Confirm";
import { ToastProvider, useToast, type ToastFn } from "./Toast";

afterEach(cleanup);

function grab<T>(hook: () => T): { current: T | null; Probe: () => null } {
  const box = { current: null as T | null, Probe: () => { box.current = hook(); return null; } };
  return box;
}

describe("useConfirm", () => {
  it("resolves true on Confirm", async () => {
    const h = grab<ConfirmFn>(useConfirm);
    render(<ConfirmProvider><h.Probe /></ConfirmProvider>);
    let answer: Promise<boolean> = Promise.resolve(false);
    act(() => { answer = h.current!({ title: "Withdraw it?", confirmLabel: "Withdraw", tone: "danger" }); });
    expect(screen.getByRole("dialog", { name: "Withdraw it?" })).toBeTruthy();
    // Focus starts on Cancel so Enter never confirms by accident.
    expect(document.activeElement?.textContent).toBe("Cancel");
    fireEvent.click(screen.getByText("Withdraw"));
    await expect(answer).resolves.toBe(true);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("resolves false on Cancel and on Escape", async () => {
    const h = grab<ConfirmFn>(useConfirm);
    render(<ConfirmProvider><h.Probe /></ConfirmProvider>);
    let a: Promise<boolean> = Promise.resolve(true);
    act(() => { a = h.current!({ title: "Sure?" }); });
    fireEvent.click(screen.getByText("Cancel"));
    await expect(a).resolves.toBe(false);

    act(() => { a = h.current!({ title: "Sure again?" }); });
    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    await expect(a).resolves.toBe(false);
  });
});

describe("useToast", () => {
  it("shows at most three, newest last, in a polite live region", () => {
    const h = grab<ToastFn>(useToast);
    render(<ToastProvider><h.Probe /></ToastProvider>);
    act(() => { ["one", "two", "three", "four"].forEach((m) => h.current!(m)); });
    expect(screen.queryByText("one")).toBeNull();
    expect(screen.getByText("four")).toBeTruthy();
    expect(screen.getByText("four").parentElement?.getAttribute("aria-live")).toBe("polite");
    act(() => { h.current!("broke", { tone: "error" }); });
    expect(screen.getByRole("alert").textContent).toBe("broke");
  });
});
