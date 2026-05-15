# Smoke tests — recent rollouts

End-to-end scenarios that exercise everything shipped between PR #22
and the current head. Run them in a browser against a real deploy (or
a `docker-compose up` of the full stack); the Go unit tests don't
cover the cross-page WS plumbing, the audio chime, or the React state
machines that string everything together.

Each scenario needs the listed number of distinct authenticated
accounts. Open one Chrome window per account; the easiest setup is
one regular window + N incognito windows (different sessions per
window). Sign each in, leave them on the homepage, then follow the
scenario.

## Conventions

- **A**, **B**, **C** = three signed-in test accounts.
- "Window A" = the browser window logged in as account A.
- "Press" = literal click. "Wait" = pause until the UI updates.
- Pass criteria are written in the **bold** lines under each step.

---

## 1. Rematch unanime (PR #23)

**Goal:** verify that both players must accept before a rematch game
is created, and that both are redirected automatically.

| Account count | 2 |
| Time | ~3 minutes |

1. Both A and B click "1 contre 1" on the homepage.
2. Wait for the match. Both windows land on the same `/game/{id}`.
   **Pass:** the URL is identical in both windows; each sees the
   other's name on a seat.
3. Play out the game until one wins (resign is the fastest — click
   "Forfait" in the sidebar of either window).
4. The end-of-game modal appears on both sides. Press "Proposer une
   revanche" in window A.
   **Pass:** window A's modal now shows "Revanche proposée — en
   attente : {B's name}" with a "Annuler" button.
5. Window B sees a banner inside the modal: "{A's name} propose une
   revanche" with Accepter / Refuser buttons.
   **Pass:** the banner appears within ~2 s (WS state push).
6. Press "Accepter" in window B.
   **Pass:** B is immediately redirected to the new game's URL.
   Within ~2 s, A is also redirected (via the WS state event that
   surfaces `rematchGameId`).
7. Both windows now show the fresh game in waiting status, ready to
   play.
   **Pass:** the URL is identical in both windows; the seats are the
   same two accounts as before; the board is empty.

### Variant 1a — Decline

Repeat steps 1-4. Then in window B press "Refuser" instead of
"Accepter". **Pass:** A's modal reverts to "Proposer une revanche"
(offer cleared on both sides).

### Variant 1b — Cancel

Repeat steps 1-4. Then in window A press "Annuler". **Pass:** B's
"Accepter / Refuser" banner disappears; A's modal reverts to "Proposer
une revanche".

### Variant 1c — Bot pre-accepted

Create a private game (A clicks "Partie privée"). Add a bot to seat 2
via the lobby. Start the game. Resign as A. Press "Proposer une
revanche". **Pass:** the rematch game is created immediately (no wait
for the bot to "accept") and A is redirected.

---

## 2. Invitation cross-page (PR #25)

**Goal:** verify that an invited user sees the toast even when not on
the game page, and can accept or decline from anywhere.

| Account count | 2 |
| Time | ~2 minutes |

1. A creates a private game (homepage → "Partie privée"). Lands on
   `/game/{id}` with one seat occupied.
2. A presses "+ Inviter" on an empty seat, searches for B's display
   name, clicks B in the result list.
   **Pass:** the seat now shows B's name with an "En attente" badge.
3. B is on the homepage (or `/leaderboard`, or `/profile` — anywhere
   that isn't `/game/{id}`).
   **Pass:** within ~1 s, a toast appears bottom-right of B's window:
   "{A's name} t'invite à jouer" with Accepter / Refuser.
   **Pass:** a notification chime plays (after at least one click on
   the page — browsers gate AudioContext on user interaction).
   **Pass:** the user-nav bell in B's header shows a badge with "1".
4. B opens the bell dropdown by clicking the bell icon.
   **Pass:** the dropdown lists the same invitation with the same
   actions.
5. B presses "Accepter" (from either the toast or the dropdown).
   **Pass:** B is navigated to `/game/{id}`; the seat that was
   reserved is now occupied by B; the game flips to "playing".
   **Pass:** the bell badge disappears.

### Variant 2a — Decline

Repeat steps 1-3. Then B presses "Refuser" on the toast. **Pass:** the
toast disappears; on A's window the seat returns to "Siège vide"
within ~1 s.

### Variant 2b — Host cancels while toast is open

Repeat steps 1-3. Then A clicks "× Annuler l'invitation" on the seat.
**Pass:** B's toast and bell badge disappear without B taking any
action.

### Variant 2c — Already on the game page

Repeat steps 1-2, but have B navigate to `/game/{id}` manually before
A invites. After the invitation is sent, **pass:** B does NOT see a
toast (the inline Accepter/Refuser controls on the seat itself take
over); the bell does NOT badge.

### Variant 2d — Stack of invitations

A invites B on seat 2, then immediately re-invites on seat 3 of a
different game. **Pass:** B sees TWO toasts stacked (newest on top);
the bell badge reads "2". Closing one (accept or decline) leaves the
other intact.

---

## 3. Matchmaking 3-6 joueurs avec ETA (PR #24 + #26)

**Goal:** verify that the multi matchmaker forms partial groups after
the threshold, and that all queued users see a live count + ETA.

| Account count | 3 to 6 |
| Time | ~30 seconds per scenario |

### Scenario 3a — 3 joueurs

1. A, B, C each click "Multijoueur" on the homepage within a 2-second
   window of each other.
2. Watch the subtitle on each "Recherche en cours…" button.
   **Pass:** within ~2 s, the subtitle reads "3/6 joueurs en attente
   — démarre dans 20s" (or close — the exact ETA depends on which
   tick was first).
3. The ETA counts down each tick (~1.5 s resolution).
4. At T ≈ 21 s after the first click, the matcher fires.
   **Pass:** all 3 windows are navigated to the same `/game/{id}` in
   playing status with exactly 3 occupied seats.
   **Pass:** a notification chime plays in each window on
   `match_found`.

### Scenario 3b — 4 joueurs

Same as 3a with D added. **Pass:** "4/6 joueurs en attente — démarre
dans 10s"; matches at T ≈ 11 s.

### Scenario 3c — 5 joueurs

Same with E. **Pass:** "5/6 — démarre dans 5s"; matches at T ≈ 6 s.

### Scenario 3d — 6 joueurs

Same with F. **Pass:** matches almost immediately (≤ 2 s) since the
threshold for 6 is 0.

### Scenario 3e — 1v1 inchangé

Two accounts click "1 contre 1". **Pass:** matches within a tick or
two (subject to rating proximity).

### Scenario 3f — Cancel mid-wait

Repeat 3a, but at T ≈ 10 s one of the queued users presses the
"Recherche en cours…" button (toggling cancel). **Pass:** that
window's status returns to idle; the remaining two see the count
drop to "2/6 joueurs en attente — il faut au moins 3 pour démarrer".

---

## 4. Last-move ring (PR #23)

**Goal:** verify the chess.com-style amber ring on the last placed
stone, both during live play and on the replay timeline.

| Account count | 2 |
| Time | ~1 minute |

1. Two accounts join a private game and start it (any path).
2. A plays the first move.
   **Pass:** an amber ring appears around A's stone on both windows.
3. B plays.
   **Pass:** the ring moves to B's stone on both windows; A's stone no
   longer has a ring.
4. Continue for several moves.
   **Pass:** the ring always tracks the most recently placed stone.
5. After the game ends, open the replay panel.
6. Scrub the replay step backward.
   **Pass:** the ring tracks the replay's "last move" (driven by
   `lastMoveAt(steps, step)`, separate from the live `game.lastMove`).

---

## 5. JWT refresh persistence (post-PR #26)

**Goal:** verify the persistent user socket survives a token refresh.

| Account count | 1 |
| Time | ~1 hour (or use a debug short-lived token) |

The default Supabase JWT TTL is 1 hour; the auto-refresh fires a few
minutes before expiry. For a faster test, override the project's JWT
expiry in the Supabase dashboard to ~5 minutes.

1. Sign in. Open DevTools → Application → Local Storage; find the
   Supabase session entry, note the `access_token` and `expires_at`.
2. Stay on the homepage. Wait until `expires_at` is in the past —
   roughly the TTL minus 5 minutes (Supabase pre-refreshes).
3. Watch DevTools → Network → WS. Filter for `/ws/lobby`.
   **Pass:** when the refresh fires, the WS connection closes and a
   new one opens (visible in the network tab). The new connection's
   `?access_token=` query param is different from the previous one.
4. Get a second account to invite this one to a game.
   **Pass:** the toast appears as usual, proving the WS is alive
   under the new token.

---

## What this plan does NOT cover

- **Multi-pod fan-out** (NOTIFY across pods). Validate manually by
  running two server instances against the same Postgres and ensuring
  events propagate, or accept the risk for single-pod deploys.
- **Backplane reconnect** when Postgres restarts.
- **Disconnect grace + forfeit timeout** under real packet loss.
- **Bot moves** — covered by unit tests but not exercised here.
- **Mobile layout / touch interactions** — Chromium emulation only
  catches ~80% of mobile-specific issues; verify on a real device for
  any UI change that touches the game board.
