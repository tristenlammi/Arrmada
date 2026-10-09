// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Menu } from "./Menu";

afterEach(cleanup);

function setup() {
  const onSign = vi.fn();
  render(
    <Menu
      trigger="T"
      label="Account"
      items={[
        { label: "Profile", onSelect: () => {} },
        { label: "Disabled", onSelect: () => {}, disabled: true },
        { label: "Password", onSelect: () => {} },
        { label: "Sign out", onSelect: onSign, tone: "danger" },
      ]}
    />,
  );
  const trigger = screen.getByRole("button", { name: "Account" });
  return { trigger, onSign };
}

const key = (k: string) => fireEvent.keyDown(document.activeElement!, { key: k });

describe("Menu", () => {
  it("opens with aria-expanded and focuses the first item", () => {
    const { trigger } = setup();
    expect(trigger.getAttribute("aria-haspopup")).toBe("menu");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(document.activeElement?.textContent).toBe("Profile");
  });

  it("moves with the arrow keys and Home/End, skipping disabled items and wrapping", () => {
    const { trigger } = setup();
    fireEvent.click(trigger);
    key("ArrowDown");
    expect(document.activeElement?.textContent).toBe("Password"); // skipped Disabled
    key("End");
    expect(document.activeElement?.textContent).toBe("Sign out");
    key("ArrowDown");
    expect(document.activeElement?.textContent).toBe("Profile"); // wrapped
    key("ArrowUp");
    expect(document.activeElement?.textContent).toBe("Sign out");
    key("Home");
    expect(document.activeElement?.textContent).toBe("Profile");
  });

  it("closes on Escape and returns focus to the trigger", () => {
    const { trigger } = setup();
    fireEvent.click(trigger);
    key("Escape");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it("closes on a click outside", () => {
    const { trigger } = setup();
    fireEvent.click(trigger);
    fireEvent.mouseDown(document.body);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("runs the item and closes", async () => {
    const { trigger, onSign } = setup();
    fireEvent.click(trigger);
    fireEvent.click(screen.getByRole("menuitem", { name: "Sign out" }));
    expect(onSign).toHaveBeenCalledTimes(1);
    await Promise.resolve();
    expect(screen.queryByRole("menu")).toBeNull();
  });
});
