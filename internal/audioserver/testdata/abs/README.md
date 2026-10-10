# Audiobookshelf reply fixtures

`TestRepliesMatchAudiobookshelf` (../../shape_test.go) plays the conversation in
`<version>/steps.json` against Arrmada's audiobook server and checks that every reply has
every key and JSON kind Audiobookshelf's reply (`<version>/<step>.json`) has. Values don't
matter. Deliberate differences are listed in `allowed_diffs.txt`, each with its reason; a
line that stops matching anything fails `TestAllowedDiffsAreNotStale`.

**2.37.1 is synthesised, not captured.** It was written by reading the Audiobookshelf
server source (advplyr/audiobookshelf @ 743d9152, v2.37.1): each route's controller and
serialiser, followed by hand. `steps.json` notes every judgement call. Replace it with a
real capture as soon as you can.

## Capturing real replies

1. Start a throwaway Audiobookshelf pinned to the version Arrmada reports
   (`ServerVersion` in ../../absjson.go). Never use a real one:

       docker run --rm -p 13380:80 -v "$PWD/abs-books:/audiobooks" ghcr.io/advplyr/audiobookshelf:2.37.1

2. In its web page create the root user, an ordinary user, and a Book library on
   `/audiobooks` with ONE public-domain book: the LibriVox mp3s plus the Project Gutenberg
   EPUB of the same title, with a series, a narrator, a genre and a cover. Let the scan
   finish.
3. From the repository root (the credentials stay in your shell, never in a file):

       ABS_URL=http://localhost:13380 ABS_USER=<ordinary user> ABS_PASS=<password> go run ./cmd/abs-capture

   It writes `<version>/` here, scrubbing tokens, JWTs, cookies, usernames, emails,
   hostnames, IP addresses and folder paths.
4. Read the diff of the new folder before committing, to be sure nothing private slipped
   through. If the version is the same as a synthesised folder, the capture replaces it.
5. `go test ./internal/audioserver -run 'TestRepliesMatchAudiobookshelf|TestAllowedDiffsAreNotStale'`
   then fix the replies, or list a deliberate difference in `allowed_diffs.txt`.
