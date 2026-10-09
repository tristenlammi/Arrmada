// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { Modal, openModalCount } from "./Modal";

afterEach(() => {
  cleanup();
  document.body.style.overflow = "";
});

const tab = (shift = false) => fireEvent.keyDown(document.activeElement ?? document.body, { key: "Tab", shiftKey: shift });
const esc = () => fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });

describe("Modal", () => {
  it("is a named dialog portalled to body, focused on open", () => {
    render(<div className="clip"><Modal onClose={() => {}} title="Hello"><button>One</button></Modal></div>);
    const dlg = screen.getByRole("dialog", { name: "Hello" });
    expect(dlg.getAttribute("aria-modal")).toBe("true");
    expect(dlg.closest(".clip")).toBeNull(); // escaped the page's container
    expect(document.activeElement).toBe(dlg);
  });

  it("wraps Tab and Shift+Tab inside the panel", () => {
    render(<Modal onClose={() => {}} ariaLabel="x"><button>First</button><button>Middle</button><button>Last</button></Modal>);
    const first = screen.getByText("First"), last = screen.getByText("Last");
    last.focus();
    tab();
    expect(document.activeElement).toBe(first);
    tab(true);
    expect(document.activeElement).toBe(last);
  });

  it("closes the topmost modal on Escape only, and restores scroll after both close", () => {
    function Stack() {
      const [outer, setOuter] = useState(true);
      const [inner, setInner] = useState(true);
      return (
        <>
          {outer && <Modal onClose={() => setOuter(false)} ariaLabel="outer"><p>sheet</p></Modal>}
          {inner && <Modal onClose={() => setInner(false)} ariaLabel="inner"><p>confirm</p></Modal>}
        </>
      );
    }
    document.body.style.overflow = "auto";
    render(<Stack />);
    expect(openModalCount()).toBe(2);
    expect(document.body.style.overflow).toBe("hidden");

    esc();
    expect(screen.queryByRole("dialog", { name: "inner" })).toBeNull();
    expect(screen.getByRole("dialog", { name: "outer" })).toBeTruthy();
    expect(document.body.style.overflow).toBe("hidden"); // still one open

    esc();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.body.style.overflow).toBe("auto");
    expect(openModalCount()).toBe(0);
  });

  it("returns focus to the opener when it closes", () => {
    function Opener() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button onClick={() => setOpen(true)}>Open</button>
          {open && <Modal onClose={() => setOpen(false)} ariaLabel="m"><button>Inside</button></Modal>}
        </>
      );
    }
    render(<Opener />);
    const opener = screen.getByText("Open");
    opener.focus();
    fireEvent.click(opener);
    expect(document.activeElement).not.toBe(opener);
    esc();
    expect(document.activeElement).toBe(opener);
  });

  it("leaves an autoFocus child focused, and still restores to the opener", () => {
    function Opener() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button onClick={() => setOpen(true)}>Open</button>
          {/* eslint-disable-next-line jsx-a11y/no-autofocus */}
          {open && <Modal onClose={() => setOpen(false)} ariaLabel="m"><input aria-label="Name" autoFocus /></Modal>}
        </>
      );
    }
    render(<Opener />);
    const opener = screen.getByText("Open");
    opener.focus();
    fireEvent.click(opener);
    expect(document.activeElement).toBe(screen.getByLabelText("Name"));
    esc();
    expect(document.activeElement).toBe(opener);
  });

  it("restores body overflow on unmount", () => {
    document.body.style.overflow = "scroll";
    const { unmount } = render(<Modal onClose={() => {}} ariaLabel="m" />);
    expect(document.body.style.overflow).toBe("hidden");
    unmount();
    expect(document.body.style.overflow).toBe("scroll");
  });

  it("ignores Escape and the backdrop when not dismissible", () => {
    const onClose = vi.fn();
    render(<Modal onClose={onClose} ariaLabel="m" dismissible={false} />);
    esc();
    fireEvent.mouseDown(screen.getByRole("presentation"));
    fireEvent.click(screen.getByRole("presentation"));
    expect(onClose).not.toHaveBeenCalled();
  });

  it("keeps clicks from bubbling to the component that opened it", () => {
    const outer = vi.fn();
    render(
      // eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions
      <div onClick={outer}>
        <Modal onClose={() => {}} ariaLabel="m"><button>Confirm</button></Modal>
      </div>,
    );
    fireEvent.click(screen.getByText("Confirm"));
    fireEvent.click(screen.getByRole("presentation"));
    expect(outer).not.toHaveBeenCalled();
  });

  it("closes on a backdrop click but not on a click inside the panel", () => {
    const onClose = vi.fn();
    render(<Modal onClose={onClose} ariaLabel="m"><button>In</button></Modal>);
    fireEvent.mouseDown(screen.getByText("In"));
    fireEvent.click(screen.getByText("In"));
    expect(onClose).not.toHaveBeenCalled();
    const backdrop = screen.getByRole("presentation");
    fireEvent.mouseDown(backdrop);
    fireEvent.click(backdrop);
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
