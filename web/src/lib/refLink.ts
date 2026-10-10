// Addresses for things in Discover, so a poster, a notification and a shared link all
// open the same place.

// titlePath is a movie's or show's own address: /discover/movie/603, /discover/series/1399.
// The segment is the API's media_type, so it maps 1:1 to /api/v1/media/<media>/<id>.
export function titlePath(media: "movie" | "series", tmdbId: number): string {
  return `/discover/${media}/${tmdbId}`;
}

// A book reference ends in what the notice was about, after the book's key: a decision
// (":approved:<unix>", ":declined:<unix>") and/or the format that arrived (":audiobook").
// The key itself can hold a ':' (Hardcover's "hc:123"), so these come off the end.
const BOOK_REF_SUFFIX = /(:(approved|declined):\d+|:(audiobook|ebook|ready))$/;

// refToPath is where a notification's reference leads: the exact title, the exact book on
// Discover's Books tab, or the request itself. null for a reference it doesn't know (an
// old notice from before refs were structured), which the bell falls back to a search for.
//
//   movie:603, movie:603:approved:1700000000      → /discover/movie/603
//   series:1399, series:1399:r12:s3                → /discover/series/1399
//   book:OL1W, book:hc:123:audiobook               → /discover?tab=books&work=hc%3A123
//   request:40:new:1700000000                      → /requests?id=40
//
// Twin: RefPath in internal/requests/refpath.go (push links). Keep the two, and the test
// vectors in refLink.test.ts and refpath_test.go, in step.
export function refToPath(ref: string | undefined | null): string | null {
  if (!ref) return null;
  const at = ref.indexOf(":");
  if (at <= 0) return null;
  const kind = ref.slice(0, at);
  const rest = ref.slice(at + 1);
  switch (kind) {
    case "movie":
    case "series": {
      const id = rest.split(":")[0];
      return /^[1-9]\d*$/.test(id) ? titlePath(kind, Number(id)) : null;
    }
    case "book": {
      let key = rest;
      for (let i = 0; i < 2; i++) key = key.replace(BOOK_REF_SUFFIX, "");
      return key ? `/discover?tab=books&work=${encodeURIComponent(key)}` : null;
    }
    case "request": {
      const id = rest.split(":")[0];
      return /^[1-9]\d*$/.test(id) ? `/requests?id=${id}` : null;
    }
  }
  return null;
}
