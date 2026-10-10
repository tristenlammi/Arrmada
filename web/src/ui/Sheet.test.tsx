// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { BrowserRouter, useLocation } from "react-router-dom";
import { Sheet } from "./Sheet";

// APP-06: Back closes an open sheet instead of leaving the page, and closing it any
// other way leaves no dead history entry behind.

afterEach(() => {
  cleanup();
  document.body.style.overflow = "";
  window.history.replaceState(null, "", "/");
});

const esc = () => fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
const back = async () => { await act(async () => { window.history.back(); await new Promise((r) => setTimeout(r, 30)); }); };

function Where() {
  const loc = useLocation();
  return <output data-testid="where">{loc.pathname}{loc.search}</output>;
}

function Page({ closeOnBack = true, stacked = false }: { closeOnBack?: boolean; stacked?: boolean }) {
  const [open, setOpen] = useState(false);
  const [inner, setInner] = useState(false);
  return (
    <>
      <Where />
      <button onClick={() => setOpen(true)}>Open</button>
      {open && (
        <Sheet onClose={() => setOpen(false)} ariaLabel="outer" closeOnBack={closeOnBack}>
          <button onClick={() => setOpen(false)}>Done</button>
          {stacked && <button onClick={() => setInner(true)}>More</button>}
        </Sheet>
      )}
      {inner && <Sheet onClose={() => setInner(false)} ariaLabel="inner"><p>inner</p></Sheet>}
    </>
  );
}

function mount(props: Parameters<typeof Page>[0] = {}) {
  window.history.replaceState(null, "", "/discover?tab=series");
  render(<BrowserRouter><Page {...props} /></BrowserRouter>);
}

describe("Sheet", () => {
  it("closes on Back and stays on the page", async () => {
    mount();
    const start = window.history.length;
    const opener = screen.getByText("Open");
    opener.focus();
    fireEvent.click(opener);
    await waitFor(() => expect(window.history.length).toBe(start + 1));
    expect(screen.getByRole("dialog", { name: "outer" })).toBeTruthy();

    await back();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByTestId("where").textContent).toBe("/discover?tab=series");
    // Focus went back to what opened it.
    expect(document.activeElement).toBe(opener);
  });

  it("steps back over its own entry when closed by Escape or by the page", async () => {
    mount();
    fireEvent.click(screen.getByText("Open"));
    await waitFor(() => expect((window.history.state as { usr?: { sheets?: string[] } })?.usr?.sheets?.length).toBe(1));
    esc();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect((window.history.state as { usr?: unknown })?.usr ?? null).toBeNull());

    fireEvent.click(screen.getByText("Open"));
    await waitFor(() => expect((window.history.state as { usr?: { sheets?: string[] } })?.usr?.sheets?.length).toBe(1));
    fireEvent.click(screen.getByText("Done"));
    await waitFor(() => expect((window.history.state as { usr?: unknown })?.usr ?? null).toBeNull());
    expect(screen.getByTestId("where").textContent).toBe("/discover?tab=series");
  });

  it("closes only the top sheet when two are stacked", async () => {
    mount({ stacked: true });
    fireEvent.click(screen.getByText("Open"));
    await waitFor(() => expect((window.history.state as { usr?: { sheets?: string[] } })?.usr?.sheets?.length).toBe(1));
    fireEvent.click(screen.getByText("More"));
    await waitFor(() => expect((window.history.state as { usr?: { sheets?: string[] } })?.usr?.sheets?.length).toBe(2));

    await back();
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "inner" })).toBeNull());
    expect(screen.getByRole("dialog", { name: "outer" })).toBeTruthy();

    await back();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("adds no history entry when its state lives in the address", async () => {
    mount({ closeOnBack: false });
    const start = window.history.length;
    fireEvent.click(screen.getByText("Open"));
    await new Promise((r) => setTimeout(r, 30));
    expect(window.history.length).toBe(start);
    esc();
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
