// Addresses for things in Discover, so a poster, a notification and a shared link all
// open the same place.

// titlePath is a movie's or show's own address: /discover/movie/603, /discover/series/1399.
// The segment is the API's media_type, so it maps 1:1 to /api/v1/media/<media>/<id>.
export function titlePath(media: "movie" | "series", tmdbId: number): string {
  return `/discover/${media}/${tmdbId}`;
}
