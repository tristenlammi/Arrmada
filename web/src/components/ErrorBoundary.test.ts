import { describe, expect, it } from "vitest";
import { createElement, type ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { ErrorBoundary } from "./ErrorBoundary";

// The test environment is node (no DOM), so these drive the boundary's lifecycle
// methods directly rather than mounting it.
function boundary(resetKey: unknown) {
  const b = new ErrorBoundary({ resetKey, children: "page" });
  b.setState = ((s: object) => {
    b.state = { ...b.state, ...s };
  }) as typeof b.setState;
  return b;
}

describe("ErrorBoundary", () => {
  it("turns a thrown value into an error state", () => {
    expect(ErrorBoundary.getDerivedStateFromError(new Error("boom")).error?.message).toBe("boom");
    expect(ErrorBoundary.getDerivedStateFromError("plain string").error?.message).toBe("plain string");
  });

  it("renders the fallback card for a caught error", () => {
    const b = boundary("/movies");
    b.state = { error: new Error("field was null"), reloading: false };
    const html = renderToStaticMarkup(b.render() as ReactElement);
    expect(html).toContain("Something broke on this page");
    expect(html).toContain("field was null");
    expect(html).toContain("Reload");
    expect(html).toContain('href="/"');
  });

  it("renders children when there is no error", () => {
    const b = boundary("/movies");
    expect(b.render()).toBe("page");
  });

  it("clears the error when resetKey changes", () => {
    const b = boundary("/movies");
    b.state = { error: new Error("x"), reloading: false };
    b.componentDidUpdate({ resetKey: "/movies", children: "page" });
    expect(b.state.error).not.toBeNull();
    (b as { props: unknown }).props = { resetKey: "/series", children: "page" };
    b.componentDidUpdate({ resetKey: "/movies", children: "page" });
    expect(b.state.error).toBeNull();
  });

  it("uses a custom fallback when given one", () => {
    const b = new ErrorBoundary({ children: "page", fallback: createElement("p", null, "custom") });
    b.state = { error: new Error("x"), reloading: false };
    expect(renderToStaticMarkup(b.render() as ReactElement)).toBe("<p>custom</p>");
  });
});
