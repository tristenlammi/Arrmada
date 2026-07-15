# Notifications — Build Plan

Two audiences, one delivery engine (**Apprise**):
1. **Admin alerts** — consolidated into a new **Insights → Notifications** tab. Covers *arr acquisition
   (grab/import) AND Plex watch events (new stream, buffering). Retires the old `/notifications` page.
2. **User request-ready alerts** — in **Discover**: when a requester's request is imported and ready,
   they get an **in-app inbox** notification, plus an optional **per-user Apprise** push.

Decisions (locked with the user): Apprise = **bundled into the single app image** (no extra container);
admin notifications = **consolidated in Insights**; user delivery = **in-app inbox + optional per-user
Apprise URL**.

---

## Delivery engine — Apprise (bundled)

- The **`apprise` CLI (Python)** is installed into the runtime image (`apk add python3 py3-pip` +
  `pip install apprise`). One image, no extra container.
- A notification target is a single **Apprise URL** (`discord://id/token`, `tgram://token/chatid`,
  `mailto://…`, `ntfy://…`, `json://…` for generic webhooks — 80+ services).
- `internal/notify` delivery reworked: `deliver()` **shells out** to `apprise -t <title> -b <body> <url>`
  (via `Send()`), detecting the binary with `exec.LookPath` at startup. Existing connections' URLs are
  re-interpreted as Apprise URLs (early project → users re-enter if needed; UI links to Apprise's URL
  docs).

---

## Part 1 — Admin notifications (Insights → Notifications tab)

- **Reuse** the `notify` service + `notifications` table (CRUD connections). Extend event flags beyond
  grab/import with Plex watch events: `on_stream_start`, `on_buffering` (migration adds columns).
- **Plex watch events**: the Insights poller emits to the eventbus — `plex.stream.started` and
  `plex.buffering` (new spell) with {user, title, player, decision}. `notify.Run` subscribes + formats.
- **UI**: move connection CRUD into an Insights **Notifications** tab (name, Apprise URL, per-event
  toggles, enabled, **Test**). Remove the `/notifications` nav entry + route (retired). Backend routes can
  stay under `/api/v1/notifications` (reused by the new tab) — only the page moves.

## Part 2 — User request-ready alerts (Discover)

- **User target**: add nullable `apprise_url` to users (migration) — a user sets their own push URL in a
  Discover profile/settings affordance. Optional; in-app always works.
- **In-app inbox**: new `user_notifications` table `{id, user_id, title, body, media_type, ref, read,
  created_at}`. A bell/inbox in Discover lists them; mark-read.
- **Match import → requester**: a subscriber on `movie.downloaded` / `series.imported` / `book.imported`
  ({id}) resolves the media's TMDB/OL-key, finds matching **approved requests**, and for each requester:
  (a) inserts an in-app notification ("<title> is ready to watch"), (b) if they have an `apprise_url`,
  pushes via Apprise. Dedupe so a multi-file series import notifies once.
- **API**: `GET /me/notifications`, `POST /me/notifications/{id}/read` (+ mark-all), `PUT /me/apprise`.
- **Discover UI**: header bell with unread count → dropdown/inbox; a small "notify me" settings row for the
  Apprise URL.

---

## Phasing

| Phase | Delivers |
|---|---|
| **N0** | Apprise companion container + config; rework `notify.deliver()` to POST via Apprise; existing grab/import alerts flow through Apprise; Test works. |
| **N1** | Admin **Notifications** tab in Insights (CRUD + per-event toggles + Test); retire `/notifications` page; emit + subscribe Plex watch events (stream start, buffering). |
| **N2** | User request-ready: `apprise_url` on users + `user_notifications` table; import→request→requester matching (in-app + optional Apprise); Discover inbox bell + Apprise-URL setting. |

## Verification note

Apprise delivery to a real service needs a real target URL. Each phase verifies the plumbing (Arrmada ↔
Apprise container reachable; correct success/failure handling); a real end-to-end push is confirmed with a
real Apprise URL (e.g. the user's Discord as `discord://…`) via the Test button.
