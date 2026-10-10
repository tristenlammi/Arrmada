import { describe, expect, it } from "vitest";
import { AUDIO_APPS, appsFor, devicePlatform, newDevice } from "./audioApps";

describe("audioApps", () => {
  it("presents only apps someone has listened with as verified", () => {
    const verified = AUDIO_APPS.filter((a) => a.status === "verified").map((a) => a.id);
    expect(verified.sort()).toEqual(["lissen", "web"]);
  });
  it("recommends listening here on an iPhone, Lissen on Android", () => {
    expect(appsFor("ios")[0].id).toBe("web");
    expect(appsFor("ios").map((a) => a.id)).not.toContain("lissen");
    expect(appsFor("ios").slice(1).every((a) => a.status === "untested")).toBe(true);
    expect(appsFor("android").map((a) => a.id)).toEqual(["lissen", "web"]);
    expect(appsFor("other")[0].id).toBe("web");
  });
  it("tells the platform from the browser", () => {
    expect(devicePlatform("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)")).toBe("ios");
    expect(devicePlatform("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", true)).toBe("ios");
    expect(devicePlatform("Mozilla/5.0 (Linux; Android 14; Pixel 8)")).toBe("android");
    expect(devicePlatform("Mozilla/5.0 (Windows NT 10.0; Win64; x64)")).toBe("other");
  });
  it("spots the device that just signed in", () => {
    const known = new Set(["a"]);
    expect(newDevice([{ id: "a", created_at: 5 }], known)).toBeNull();
    expect(newDevice([{ id: "a", created_at: 5 }, { id: "b", created_at: 1 }, { id: "c", created_at: 2 }], known)?.id).toBe("c");
  });
});
