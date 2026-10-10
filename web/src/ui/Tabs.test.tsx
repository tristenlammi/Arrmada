import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { Tabs, TabPanel, rovingTarget, scrollDelta } from "./Tabs";

describe("rovingTarget", () => {
  it("moves right and left, wrapping at the ends", () => {
    expect(rovingTarget("ArrowRight", 0, 3)).toBe(1);
    expect(rovingTarget("ArrowRight", 2, 3)).toBe(0);
    expect(rovingTarget("ArrowLeft", 0, 3)).toBe(2);
    expect(rovingTarget("ArrowLeft", 2, 3)).toBe(1);
  });

  it("jumps to the first and last tab", () => {
    expect(rovingTarget("Home", 2, 5)).toBe(0);
    expect(rovingTarget("End", 0, 5)).toBe(4);
  });

  it("ignores other keys", () => {
    expect(rovingTarget("Enter", 1, 3)).toBeNull();
    expect(rovingTarget("a", 1, 3)).toBeNull();
    expect(rovingTarget("ArrowRight", 0, 0)).toBeNull();
  });
});

describe("Tabs", () => {
  const tabs = [
    { key: "a", label: "Alpha" },
    { key: "b", label: "Beta", count: 0 },
    { key: "c", label: "Gamma" },
  ] as const;

  it("is an ARIA tablist with only the current tab in the Tab order", () => {
    const html = renderToStaticMarkup(<Tabs tabs={tabs} value="b" onChange={() => {}} idPrefix="t" label="Sections" />);
    expect(html).toContain('role="tablist"');
    expect(html).toContain('aria-label="Sections"');
    expect(html.match(/role="tab"/g)).toHaveLength(3);
    expect(html.match(/tabindex="0"/g)).toHaveLength(1);
    expect(html).toMatch(/id="t-tab-b"[^>]*aria-selected="true"[^>]*aria-controls="t-panel-b"[^>]*tabindex="0"/);
    expect(html).toMatch(/id="t-tab-a"[^>]*aria-selected="false"[^>]*tabindex="-1"/);
  });

  it("shows a count of zero", () => {
    const html = renderToStaticMarkup(<Tabs tabs={tabs} value="a" onChange={() => {}} idPrefix="t" label="Sections" />);
    expect(html).toMatch(/Beta<span[^>]*>0<\/span>/);
  });

  it("keeps one tab focusable when none is current", () => {
    const html = renderToStaticMarkup(<Tabs tabs={tabs} value={null} onChange={() => {}} idPrefix="t" label="Sections" />);
    expect(html.match(/tabindex="0"/g)).toHaveLength(1);
    expect(html).not.toContain('aria-selected="true"');
  });

  it("labels the panel by its tab", () => {
    const html = renderToStaticMarkup(<TabPanel idPrefix="t" value="b">body</TabPanel>);
    expect(html).toBe('<div role="tabpanel" id="t-panel-b" aria-labelledby="t-tab-b">body</div>');
  });
});

describe("scrollDelta", () => {
  const row = { left: 0, right: 375 };
  it("leaves a tab that already shows alone", () => {
    expect(scrollDelta(row, { left: 100, right: 180 })).toBe(0);
  });
  it("scrolls just far enough to reveal a tab off either edge", () => {
    expect(scrollDelta(row, { left: 400, right: 480 })).toBe(105);
    expect(scrollDelta(row, { left: -60, right: 20 })).toBe(-60);
  });
});
