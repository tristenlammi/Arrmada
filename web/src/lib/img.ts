// posterThumb rewrites a poster/cover URL to a smaller, grid-appropriate size.
// Grid cells are ~150px wide, so a full w500 TMDB poster ships ~3× the bytes it
// needs; w342 stays crisp on retina while roughly halving the download. Detail
// pages keep the stored full-size URL. Handles TMDB and Open Library; anything
// else is returned unchanged.
export function posterThumb(url?: string): string | undefined {
  if (!url) return url;
  // TMDB: https://image.tmdb.org/t/p/{w500|original}/xxx.jpg → w342
  const tmdb = url.replace(/\/t\/p\/(?:w\d+|original)\//, "/t/p/w342/");
  if (tmdb !== url) return tmdb;
  // Open Library: covers.openlibrary.org/b/id/123-L.jpg → -M.jpg
  return url.replace(/-L\.(jpg|jpeg|png)$/i, "-M.$1");
}
