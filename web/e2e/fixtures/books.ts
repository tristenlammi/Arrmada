import type { BookDiscoverCard, BookMeta } from "../../src/lib/api";

// Discover → Books. Two cards: one not in the library (the Read / Listen / Both control)
// and one held only as an ebook (its card offers "Request audiobook").
const cover = (n: number) => `https://covers.openlibrary.org/b/id/e2e-${n}-L.jpg`;

export const requestableBook: BookDiscoverCard = {
  key: "OL9001W", title: "The Salt Road", author: "Mara Quill", year: 2021, cover_url: cover(1),
  in_library: false, has_file: false, requested: false,
};

export const ebookOnlyBook: BookDiscoverCard = {
  key: "OL9002W", title: "Ropes and Rigging", author: "Hal Yard", year: 2018, cover_url: cover(2),
  in_library: true, has_file: true, requested: false,
  has_ebook: true, has_audiobook: false, want_ebook: true, want_audiobook: false,
};

export const bookCards: BookDiscoverCard[] = [requestableBook, ebookOnlyBook];

export const browse = { books: bookCards };

// detail answers the request sheet's lookup; the owner's default is the ebook.
export function detail(key: string): BookMeta {
  const c = bookCards.find((b) => b.key === key) ?? requestableBook;
  return {
    key: c.key, title: c.title, author: c.author, year: c.year, cover_url: c.cover_url,
    description: `${c.title} is a fixture used by the browser smoke tests.`, subjects: ["Sea stories"],
    default_book_formats: "ebook",
  };
}
