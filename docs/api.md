# Calling Arrmada from scripts (personal API keys)

A **personal API key** lets a script or another app — an [n8n](https://n8n.io) workflow,
Home Assistant, a cron job with `curl` — call Arrmada as you: search for a title, request it,
check on your requests, approve them if you're staff.

These are *your* keys, made on your **Me** page. They're not the keys Arrmada itself uses
for outside services such as TMDB (those are in Settings → Connections), not the
dashboard widget's own key (`arrw_…`, see [Homepage dashboard](homepage.md)), and not
the n8n webhook that Arrmada sends alerts to ([Alerts to n8n](n8n.md) covers that
direction).

## Make a key

1. Open **Me** (your name in the sidebar, or the Me tab on a phone) → **Personal API keys**
   → **New API key**.
2. Give it a name that says what uses it ("n8n", "Home Assistant").
3. Pick its **access**:
   - **Read only** — `GET` requests only: it can look things up and read your requests,
     never change anything.
   - **Full** — whatever your account may do, on the routes keys can use (below).
4. Pick when it **expires**: never, or in 30, 90 or 365 days.
5. Prove it's you. Type your password, and with two-factor sign-in on, a current code from
   your authenticator app (or a recovery code) too. An account that only signs in with Plex
   or Jellyfin has no password to ask for, so it can make a key only within 15 minutes of
   signing in; after that, the card asks you to sign out and sign in again first.
6. **Copy the key now.** It starts with `arrk_` and is shown this once: Arrmada keeps only a
   fingerprint of it (a SHA-256 hash), so nobody — the admin included — can show it again.
   Lost it? Revoke it and make another.

The list shows each key's name, its start and last four characters (`arrk_…7f3q`), its
access, when it was made, when it was last used and roughly from where, and when it
expires. **Revoke** stops it on the very next request.

An admin sees everyone's keys under **Settings → People & access → Personal API keys** —
whose, what access and when last used, never from where — and can revoke any of them.

## Send it

Put the key in a header on every request:

```
X-Api-Key: arrk_…
```

or

```
Authorization: Bearer arrk_…
```

Never in the address. A request with a key in its query string (`?apikey=arrk_…`) is refused
with `400`, whatever the parameter is called: addresses end up in proxy logs and browser
history.

## What a key may do

A key acts as you, with two limits on top:

- **Its access.** A read-only key gets `403` from anything but `GET`.
- **The role you had when you made it.** If you're promoted later, old keys don't grow with
  you; make a new one. If you're demoted, your keys follow the lower role at once.

Some things end your keys for good:

- **Changing your password** (or an admin setting a new one for you) revokes every key you
  hold, as well as signing out your other devices: a new password is what you do when you
  think someone else knew the old one.
- **"Sign out everywhere"** in People & access (an admin) revokes them too.
- **Deleting the account** deletes its keys; merging a duplicate account revokes its keys.

Disabling an account only pauses its keys: they're refused while it's disabled and work
again if it's turned back on, like the rest of what the account had.

What each route shows you is exactly what your signed-in browser would see: a requester's
key lists only their own and followed requests, without other people's names or notes.

### The routes keys can use

Keys reach only the part of Arrmada built for callers from outside your home network, and of
that only what a script has a use for. Everything else answers a key with `403` — including
signing in and out, sessions, passwords and two-factor sign-in, making or revoking keys (a
key can't make more keys), your account settings, the audiobook player, the support bundle,
logs, outside services' API keys and every admin page.

| What | Routes |
|---|---|
| Is it up, who am I | `GET /api/health`, `GET /api/v1/status`, `GET /api/v1/auth/me` |
| Discover (movies and TV) | `GET /api/v1/discover/search?q=…`, `/discover/trending`, `/discover/popular`, `/discover/upcoming`, … and `GET /api/v1/media/{movie\|series}/{tmdb_id}` |
| Discover (books) | `GET /api/v1/books/discover/search?q=…`, `/books/discover/trending`, … |
| Requests | `GET /api/v1/requests`, `POST /api/v1/requests`, `GET` and `DELETE /api/v1/requests/{id}`, `DELETE /api/v1/requests/{id}/subscription` |
| Requests (staff) | `POST /api/v1/requests/{id}/approve`, `/decline`, `/preview`, `POST /api/v1/requests/bulk` |
| Your limits and shelf | `GET /api/v1/me/quota`, `GET /api/v1/me/books` |
| Reporting a problem | `GET` and `POST /api/v1/issues` |
| Your inbox | `GET /api/v1/me/notifications`, `POST /api/v1/me/notifications/read-all`, `POST /api/v1/me/notifications/{id}/read` |
| Library changes (staff) | `GET /api/v1/history`, `GET /api/v1/movies/{id}`, `GET /api/v1/series/{id}`: what a media server's scanner reads to rescan only what changed |

The exact list is the source of truth: routes marked `key` in
[`internal/httpapi/testdata/routes.golden`](../internal/httpapi/testdata/routes.golden), which
are the ones in [`internal/httpapi/apikey_routes.go`](../internal/httpapi/apikey_routes.go).
The rule for adding one: it must be open to callers from outside the home network (marked
`ext` there; staff routes can be, as staff are never limited to the outside view), it must be
something a script needs, and it must not be about signing in or account security. A test
holds the list to that.

There's no OpenAPI description yet; the golden file above lists every route and who may
call it.

### Mistakes and limits

- A wrong, revoked or expired key gets `401`. The same wrong key tried 10 times from one
  address in 15 minutes then gets `429` (with `Retry-After`) for a while; only that key is
  slowed, so a stale key in one script doesn't stop the other keys behind the same address
  (your n8n host, a shared connection). Fifty wrong keys of any kind from one address in 15
  minutes and every wrong key from there gets `429`. A right key always works. None of this
  touches signing in with a password.
- A route keys can't use, or a read-only key on a change, gets `403`.
- A key's last use is recorded at most every ten minutes, or after a minute when it's used
  from a different network, so "last used" can be a few minutes behind.

### Planned

The owner has deferred an **Admin** access level: a staff account's key that also reaches
the rest of the staff library routes — adding and searching titles, the queue, wanted — the
way a script drives Radarr's or Sonarr's API. Reading history and a title's detail is
already open to staff keys (above). It would never reach security or settings (sign-in,
sessions, passwords, two-factor, keys, people, outside services' keys, folders, backups,
logs). Until then, keys reach only the routes above.

## Titles: `title` and `display_title`

Movies and shows carry two titles. `title` is always TMDB's English title — what Arrmada
matches releases by and names files and folders after — so a script can rely on it whatever
anyone picks in the app. `display_title` is the name the app shows (Settings → Library →
Display titles, or the title's own pick on its page): the original Greek title of a Greek
film, say. When nothing changes it, it is the same as `title`.

- `GET /api/v1/movies` and `GET /api/v1/series` rows and `GET /api/v1/movies/{id}` and
  `GET /api/v1/series/{id}` carry `display_title`; the detail and full rows also carry
  `title_override` when the title has its own pick. The movie list's `sort_title` follows
  `display_title`.
- Requests (`GET /api/v1/requests`) and Discover cards carry `display_title` only when the
  title is in the library under another name; `title` stays what was asked for (TMDB's).
- Feeds made for reading — the calendar (and its iCal feed), Wanted, history, the blocklist,
  Activity's download items — give the shown title as their `title`. So do the
  `movie.imported` and `episodes.imported` webhooks ([n8n](n8n.md)); identify the title by
  `tmdb_id` or the Arrmada id, never by its name.
- Staff: `PUT /api/v1/movies/{id}/display-title` (and `/series/{id}/display-title`) with
  `{"title": "…"}` sets a title's own name (at most 200 characters, one line), `{"title": ""}`
  clears it; the answer is `{id, title, display_title, title_override}`.
  `GET /api/v1/movies/{id}/title-choices` (and `/series/…`) lists the names TMDB has for it.
  These are staff routes, not open to personal API keys.
- The setting is `display_title_mode` (`english`, `original` or `language`),
  `display_title_language` (a language code, for `language`) and `display_title_original_in`
  (language codes) in `GET`/`PUT /api/v1/settings`.
- Overviews work the same way: `overview` is always TMDB's English overview, and
  `display_overview` is the overview in the setting's language, sent only when the mode is
  `language` and TMDB has the overview translated (otherwise it is absent and `overview` is
  what the app shows). `GET /api/v1/movies/{id}`, `GET /api/v1/series/{id}`, the Discover
  detail (`GET /api/v1/media/{media}/{id}`, and its `card`) and the trending cards
  (`GET /api/v1/discover/trending`) carry it. The `english` and `original` modes and
  `display_title_original_in` never change overviews; seasons and episodes keep English ones.

### Books: details edited by hand

A book's details can be edited by hand over what the catalogue (Hardcover or Open Library)
matched. Unlike movies and shows, a book's `title`, `author`, `series_name`,
`series_position`, `description`, `year`, `subjects` and `audiobook_narrator` are always the
**shown** values — the edit when there is one — and so are `subtitle`, `language` (a
language code), `isbn` (bare digits) and `publisher`, which only an edit fills. File naming,
series grouping, OPDS and listening apps use them too. A book's identity is its Arrmada `id`
and catalogue keys (`ol_key`), which an edit never changes.

- `GET /api/v1/books/{id}` carries `edited`: each hand-edited field with the catalogue's
  value under it, e.g. `{"title": "Harry Potter and the Philosopher's Stone"}`. A field not
  in it isn't edited.
- Staff: `PUT /api/v1/books/{id}/details` with `{"set": {"title": "…", "series_position": 1.5,
  "subjects": ["Fantasy"], "isbn": "978-0-590-35340-3"}, "reset": ["author"]}` or
  `{"reset_all": true}`. Fields: `title`, `subtitle`, `author`, `series_name`,
  `series_position`, `description`, `year`, `language`, `subjects`, `isbn`, `narrator`,
  `publisher`. Setting a field to the catalogue's value is a reset. A refused value (an ISBN
  whose check digit doesn't match, a control character, too long) answers `400` with why. The
  answer is `{book, changed, rename_available}`; `rename_available` says the edit changed
  where the book naming would put its files — nothing is renamed until
  `GET /api/v1/books/organize?book_id={id}` is previewed and `POST /api/v1/books/organize`
  with `{"token": …, "book_id": {id}}` applies it.
- Staff: `POST /api/v1/books/{id}/rematch` takes `"edits": "keep"` (the default) or `"clear"`.
- These are staff routes, not open to personal API keys.

## Examples

These use `curl` with Arrmada at `http://192.168.1.10:7878` and the key in `$ARRMADA_KEY`.

**Search for a movie or show:**

```sh
curl -s -H "X-Api-Key: $ARRMADA_KEY" \
  "http://192.168.1.10:7878/api/v1/discover/search?q=dune"
```

The answer is `{"items": [...], "page": 1, "total_pages": …}`; each item has `media_type`
(`movie` or `series`), `tmdb_id`, `title` and `year`.

**Request (add) a movie** — a full key; a staff account's request, or one from an account
set to auto-approve, goes straight to downloading:

```sh
curl -s -X POST -H "X-Api-Key: $ARRMADA_KEY" -H "Content-Type: application/json" \
  -d '{"media_type": "movie", "tmdb_id": 438631}' \
  http://192.168.1.10:7878/api/v1/requests
```

For a show, use `"media_type": "series"` and optionally `"seasons": [1, 2]` (leave it out
for the whole show). For a book, `"media_type": "book"` with the `ol_key` from the books
search. If it was already requested you're added as a follower (`"subscribed": true`). Request
limits apply as in the app (`429` with `"code": "quota"`).

**List your requests:**

```sh
curl -s -H "Authorization: Bearer $ARRMADA_KEY" \
  "http://192.168.1.10:7878/api/v1/requests?status=pending"
```

**Approve one** (a manager's or admin's full key):

```sh
curl -s -X POST -H "X-Api-Key: $ARRMADA_KEY" -H "Content-Type: application/json" \
  -d '{}' http://192.168.1.10:7878/api/v1/requests/42/approve
```

### In n8n

Use an **HTTP Request** node. For **Authentication** pick *Generic Credential Type* →
*Header Auth*, with **Name** `X-Api-Key` and **Value** your key, so the key lives in n8n's
credential store rather than in the workflow. Then set the method and URL as above (and, for
a request, **Send Body** → JSON).

### In Home Assistant

A read-only key is enough for a sensor, e.g. how many of your requests are waiting:

```yaml
rest:
  - resource: http://192.168.1.10:7878/api/v1/requests?status=pending
    headers:
      X-Api-Key: !secret arrmada_key
    scan_interval: 600
    sensor:
      - name: Arrmada pending requests
        value_template: "{{ value_json.total }}"
```

## Keeping keys safe

- Treat a key like a password: anyone who has it can do what it allows, as you.
- Prefer **Read only** and an expiry for anything that only reads.
- One key per thing that uses it, so you can revoke one without breaking the others.
- If a key may have leaked, revoke it. Keys never appear in Arrmada's logs, and the support
  bundle blanks anything shaped like one.
