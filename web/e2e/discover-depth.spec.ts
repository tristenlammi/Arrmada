import { test, expect } from "./mockApi";
import { cards, collection, person, readyRequests, requestableCard } from "./fixtures/discover";

// Phase 8's deeper Discover on a desktop: the payoff rows, 'See all' grids, paged search,
// person pages and collection pages, with Back retracing each step.

test.describe("requester on a desktop", () => {
  test.use({ persona: "requester" });

  // REQ-18: both payoff rows, the ready one with Watch on Plex.
  test("Ready for you and Recently added", async ({ page, api }) => {
    await page.goto("/discover");
    await api.quiet();
    await expect(page.getByRole("heading", { name: "Ready for you" })).toBeVisible();
    await expect(page.getByRole("link", { name: `Watch ${readyRequests.requests[0].title} on Plex` })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Recently added" })).toBeVisible();
  });
});

test.describe("browse grids on a desktop", () => {
  test.use({ persona: "requester" });

  // REQ-19: a row's See all opens its whole list, which keeps loading past 20 titles.
  test("See all on Popular movies pages as it scrolls", async ({ page, api }) => {
    await page.goto("/discover");
    await api.quiet();
    await page.getByRole("link", { name: "See all: Popular movies" }).click();
    await expect(page).toHaveURL(/\/discover\/browse\?list=popular&media=movie$/);
    await expect(page.getByRole("heading", { name: "Popular movies" })).toBeVisible();
    await expect(page.getByRole("button", { name: `View details for ${requestableCard.title}` })).toBeVisible();
    await page.getByRole("button", { name: /View details for/ }).last().scrollIntoViewIfNeeded();
    await expect(page.getByRole("button", { name: "View details for Page 2 title 1", exact: true })).toBeVisible();
    expect(api.calls.some((c) => c.path === "/api/v1/discover/browse?list=popular&media=movie&page=2")).toBe(true);

    // A title opened from the grid sits over it; closing it lands back on the grid.
    await page.getByRole("button", { name: `View details for ${requestableCard.title}` }).click();
    await expect(page).toHaveURL(/\/discover\/browse\/movie\/1001\?list=popular&media=movie$/);
    await page.getByRole("dialog", { name: requestableCard.title }).getByRole("button", { name: "Close", exact: true }).click();
    await expect(page).toHaveURL(/\/discover\/browse\?list=popular&media=movie$/);
    await expect(page.getByRole("button", { name: "View details for Page 2 title 1", exact: true })).toBeVisible();
  });

  test("filters live in the address and survive a reload", async ({ page, api }) => {
    await page.goto("/discover/browse?media=movie");
    await api.quiet();
    await page.getByRole("group", { name: "Genres" }).getByRole("button", { name: "Science Fiction" }).click();
    await page.getByRole("combobox", { name: "Minimum rating" }).selectOption("7");
    await page.getByRole("textbox", { name: "From year" }).fill("1990");
    await page.getByRole("textbox", { name: "To year" }).fill("1999");
    await page.getByRole("textbox", { name: "To year" }).press("Enter");
    await expect(page).toHaveURL(/genre=878/);
    await expect(page).toHaveURL(/rating=7/);
    await expect(page).toHaveURL(/year_from=1990&year_to=1999/);
    await page.reload();
    await api.quiet();
    await expect(page.getByRole("group", { name: "Genres" }).getByRole("button", { name: "Science Fiction" })).toHaveAttribute("aria-pressed", "true");
    await expect(page.getByRole("combobox", { name: "Minimum rating" })).toHaveValue("7");
    await expect(page.getByRole("textbox", { name: "From year" })).toHaveValue("1990");
    // The reloaded grid asked for exactly the filtered first page.
    const parts = ["media=movie", "genre=878", "year_from=1990", "year_to=1999", "rating=7", "page=1"];
    expect(api.callsTo("GET", "/api/v1/discover/browse").some((c) => parts.every((part) => c.path.includes(part)))).toBe(true);
  });

  test("search results keep loading past the first page", async ({ page, api }) => {
    await page.goto("/discover?q=star");
    await api.quiet();
    await expect(page.getByRole("heading", { name: /Results for “star”/ })).toBeVisible();
    await page.getByRole("button", { name: /View details for/ }).last().scrollIntoViewIfNeeded();
    await expect(page.getByRole("button", { name: "View details for Page 2 title 1", exact: true })).toBeVisible();
    expect(api.calls.some((c) => c.path === "/api/v1/discover/search?q=star&page=2")).toBe(true);
  });
});

test.describe("people on a desktop", () => {
  test.use({ persona: "requester" });

  // REQ-20: cast tile → person → title → Back → Back.
  test("a cast tile opens the person, their credits open titles, Back retraces", async ({ page, api }) => {
    await page.goto("/discover?tab=movies");
    await api.quiet();
    await page.getByRole("button", { name: `View details for ${requestableCard.title}` }).first().click();
    const sheet = page.getByRole("dialog", { name: requestableCard.title });
    await expect(sheet).toBeVisible();
    // An old record's cast member without an id stays a plain tile.
    await expect(sheet.getByRole("button", { name: "Open Theo Keel" })).toHaveCount(0);
    await sheet.getByRole("button", { name: `Open ${person.name}` }).click();
    await expect(page).toHaveURL(new RegExp(`/discover/person/${person.id}\\?tab=movies$`));
    const personSheet = page.getByRole("dialog", { name: person.name });
    await expect(personSheet.getByRole("heading", { name: person.name })).toBeVisible();
    await expect(personSheet.getByRole("heading", { name: /Filmography/ })).toBeVisible();

    await personSheet.getByRole("button", { name: `View details for ${cards[3].title}` }).last().click();
    await expect(page.getByRole("dialog", { name: cards[3].title })).toBeVisible();
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`/discover/person/${person.id}`));
    await expect(page.getByRole("dialog", { name: person.name })).toBeVisible();
    await page.goBack();
    await expect(page.getByRole("dialog", { name: requestableCard.title })).toBeVisible();
    await page.getByRole("dialog", { name: requestableCard.title }).getByRole("button", { name: "Close", exact: true }).click();
    await expect(page).toHaveURL(/\/discover\?tab=movies$/);
  });

  test("the director links to their page, and search finds people", async ({ page, api }) => {
    await page.goto(`/discover/movie/${requestableCard.tmdb_id}`);
    await api.quiet();
    await page.getByRole("button", { name: "Rowan Helm" }).click();
    await expect(page).toHaveURL(/\/discover\/person\/5002/);
    // Not in the fixtures (a 404): the page says so instead of failing.
    await expect(page.getByText("This person can’t be shown here.")).toBeVisible();
    await page.goto("/discover");
    await api.quiet();
    await page.getByRole("textbox", { name: "Search movies and TV" }).fill("ada");
    await page.getByRole("button", { name: new RegExp(person.name) }).click();
    await expect(page).toHaveURL(new RegExp(`/discover/person/${person.id}$`));
    await expect(page.getByRole("dialog", { name: person.name })).toBeVisible();
  });
});

test.describe("collections on a desktop", () => {
  test.use({ persona: "requester" });

  // REQ-21: row → collection page → title → Back → collection → Close.
  test("a 'Complete the' row opens its collection", async ({ page, api }) => {
    await page.goto("/discover");
    await api.quiet();
    await expect(page.getByRole("heading", { name: `Complete the ${collection.name}` })).toBeVisible();
    await page.getByRole("button", { name: `See all: Complete the ${collection.name}` }).click();
    await expect(page).toHaveURL(new RegExp(`/discover/collection/${collection.id}$`));
    const sheet = page.getByRole("dialog", { name: collection.name });
    await expect(sheet.getByText("1 of 3 in the library")).toBeVisible();
    // A requester asks one title at a time: no "Request the rest".
    await expect(sheet.getByRole("button", { name: /Request the rest/ })).toHaveCount(0);
    await sheet.getByRole("button", { name: `View details for ${cards[0].title}` }).click();
    await expect(page.getByRole("dialog", { name: cards[0].title })).toBeVisible();
    await page.goBack();
    await expect(page.getByRole("dialog", { name: collection.name })).toBeVisible();
    await page.getByRole("dialog", { name: collection.name }).getByRole("button", { name: "Close", exact: true }).click();
    await expect(page).toHaveURL(/\/discover$/);
  });

  test("a movie's sheet links to its collection", async ({ page, api }) => {
    await page.goto(`/discover/movie/${requestableCard.tmdb_id}`);
    await api.quiet();
    await page.getByRole("button", { name: `Part of the ${collection.name} →` }).click();
    await expect(page).toHaveURL(new RegExp(`/discover/collection/${collection.id}$`));
    await expect(page.getByRole("dialog", { name: collection.name })).toBeVisible();
    await page.goBack();
    await expect(page.getByRole("dialog", { name: requestableCard.title })).toBeVisible();
  });
});

test.describe("collections for staff", () => {
  test.use({ persona: "admin" });

  // Staff ask for every missing released film; each goes through the normal request.
  test("Request the rest asks for each missing film", async ({ page, api }) => {
    await page.goto(`/discover/collection/${collection.id}`);
    await api.quiet();
    await page.getByRole("button", { name: "Request the rest (2)" }).click();
    await expect(page.getByText("Requested 2 films")).toBeVisible();
    const asked = api.callsTo("POST", "/api/v1/requests").map((c) => (c.body as { tmdb_id: number }).tmdb_id).sort();
    expect(asked).toEqual([cards[0].tmdb_id, cards[8].tmdb_id].sort());
  });
});
