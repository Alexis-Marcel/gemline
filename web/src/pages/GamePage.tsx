import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, ApiError } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import type { Color, Game, GameRatings, Replay } from "../api/types";
import {
  acquireMoveStream,
  acquirePresenceStream,
  acquireRatedStream,
  getSocket,
} from "../api/gameSocket";
import { useGameSocket } from "../api/ws";
import { AnonymousJoinModal } from "../components/AnonymousJoinModal";
import { Board } from "../components/Board";
import { ChatPanel } from "../components/ChatPanel";
import { ConnStatus } from "../components/ConnStatus";
import { DrawOfferAndActions } from "../components/DrawOfferAndActions";
import { GameEndModal } from "../components/GameEndModal";
import { Objectives } from "../components/Objectives";
import { ObjectivesPopover } from "../components/ObjectivesPopover";
import { RematchControls } from "../components/RematchControls";
import { ReplayControls } from "../components/ReplayControls";
import { Scoreboard } from "../components/Scoreboard";
import { SearchingForOpponent } from "../components/SearchingForOpponent";
import { SeatInviteModal } from "../components/SeatInviteModal";
import { ShareCard } from "../components/ShareCard";
import { StartButton } from "../components/StartButton";
import { UserNav } from "../components/UserNav";
import { useInPlayActions } from "../hooks/useInPlayActions";
import { useRematchFlow } from "../hooks/useRematchFlow";
import {
  clearCredentials,
  saveCredentials,
  useCredentials,
} from "../lib/credentials";
import { mergeGameSnapshot } from "../lib/gameSnapshot";
import { hapticCapture, hapticGameEnd } from "../lib/haptics";
import { cellsAtStep, lastMoveAt } from "../lib/replay";

export function GamePage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const { user } = useAuth();
  const {
    game: liveGame,
    status: wsStatus,
    attempt: wsAttempt,
  } = useGameSocket(id);
  // optimisticGame holds the server's response to the caller's own
  // mutations (postMove, draw offers, leaves, ...). It's merged with
  // liveGame via mergeGameSnapshot — see lib/gameSnapshot.ts for the
  // tie-breaking rule and the bug history that justifies it.
  const [optimisticGame, setOptimisticGame] = useState<Game | null>(null);
  const [name, setName] = useState("");
  const [joining, setJoining] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [replay, setReplay] = useState<Replay | null>(null);
  const [replayStep, setReplayStep] = useState(0);
  const [replayLoading, setReplayLoading] = useState(false);

  // Per-seat presence map fed by the shared socket. true = at least one live
  // WebSocket; false = nobody is on this seat right now; undefined = unknown
  // (we haven't received a presence event yet, default to optimistic online).
  const [presence, setPresence] = useState<Record<number, boolean>>({});

  // Per-game rating snapshot for the in-Scoreboard Elo line and the
  // end-of-game modal. We seed from an HTTP fetch on mount and let the
  // WS "rated" event overwrite once the server applies deltas — those
  // two paths converge on the same shape (api.getGameRatings == the
  // event payload), so we can replace the whole object on each update.
  const [ratings, setRatings] = useState<GameRatings | null>(null);
  // Modal dismissal flag — once the user closes the GameEndModal we
  // don't reopen it unless they navigate away and back. Lets them get
  // to the chat + replay underneath without nagging.
  const [endModalDismissed, setEndModalDismissed] = useState(false);

  // Stones captured by the most recent move, kept around briefly so the
  // Board can animate them out. Each entry has a unique key so React doesn't
  // re-use a dying ghost when a subsequent capture lands on the same cell.
  const [ghosts, setGhosts] = useState<
    Array<{ q: number; r: number; color: Color; key: string }>
  >([]);

  const game = useMemo(
    () => mergeGameSnapshot(liveGame, optimisticGame),
    [liveGame, optimisticGame],
  );

  // Creds tracks the seat token for this game id and stays reactive to
  // out-of-band writes — the lobby's rematch_ready event can save creds
  // while the page is already mounted on the new game id, and the
  // auto-join effect + WS hello pick them up automatically.
  const creds = useCredentials(id);

  // Push our seat token to the shared socket so the server can mark us as
  // online (and cancel any disconnect-grace timer that was running).
  useEffect(() => {
    const socket = getSocket(id);
    socket.setHelloToken(creds?.token ?? null);
    return () => {
      socket.setHelloToken(null);
    };
  }, [id, creds?.token]);

  // Subscribe to presence events for everyone in this game.
  useEffect(() => {
    return acquirePresenceStream(id, (seatIndex, online) => {
      setPresence((prev) => ({ ...prev, [seatIndex]: online }));
    });
  }, [id]);

  // Initial ratings fetch on mount, plus a refetch on the
  // playing→finished transition so the modal has data even if the WS
  // "rated" event was missed. The "rated" subscription below handles
  // the live case; this is the resync safety net.
  useEffect(() => {
    let cancelled = false;
    api
      .getGameRatings(id)
      .then((gr) => {
        if (!cancelled) setRatings(gr);
      })
      .catch(() => {
        /* server returns 404 for unknown games or a transient error
         * — either way the UI gracefully degrades to "no Elo info"
         * via ratings:null and the modal falls back to a generic
         * end-of-game card. */
      });
    return () => {
      cancelled = true;
    };
  }, [id]);

  // The end-of-game modal needs `applied: true` to show deltas. The
  // server emits a "rated" WS event right after ApplyRatedGame
  // commits; subscribing here is the live path. Refetch fallback is
  // below (on finished transition) in case the event arrives before
  // the modal can render.
  useEffect(() => {
    return acquireRatedStream(id, (gr) => {
      setRatings(gr);
    });
  }, [id]);

  // Subscribe to move events so we can render captured stones with a fade-out.
  useEffect(() => {
    return acquireMoveStream(id, (move) => {
      if (move.captures.length === 0) return;
      const added = move.captures.flatMap((c) =>
        c.pair.map(([q, r]) => ({
          q,
          r,
          color: c.victim,
          key: `${q},${r},${Date.now()},${Math.random()}`,
        })),
      );
      setGhosts((prev) => [...prev, ...added]);
      // Buzz on captures so the player feels the take-off even when not
      // looking at the screen mid-typing. No-op on iOS Safari (no
      // navigator.vibrate) and on desktop browsers without a haptic
      // device — see lib/haptics for the gate.
      hapticCapture();
      const keys = new Set(added.map((g) => g.key));
      window.setTimeout(() => {
        setGhosts((prev) => prev.filter((g) => !keys.has(g.key)));
      }, 600);
    });
  }, [id]);

  // Vibrate once on the playing → finished transition so the player knows
  // the result landed without having to look. Refs guard against a fresh
  // mount on a finished game (would buzz on every page load) and against
  // a state event that didn't actually flip status.
  const lastStatusRef = useRef<string | null>(null);
  useEffect(() => {
    if (!game) return;
    const prev = lastStatusRef.current;
    lastStatusRef.current = game.status;
    if (prev && prev !== "finished" && game.status === "finished") {
      hapticGameEnd();
    }
  }, [game]);

  const isMyTurn =
    !!game &&
    !!creds &&
    game.status === "playing" &&
    game.turn === creds.seatIndex;

  const onPlay = useCallback(
    async (q: number, r: number) => {
      if (!creds || !isMyTurn) return;
      setError(null);
      try {
        const res = await api.postMove(id, creds.token, q, r);
        setOptimisticGame(res.game);
      } catch (err) {
        setError(err instanceof ApiError ? err.message : "Erreur inconnue");
      }
    },
    [creds, id, isMyTurn],
  );

  // If the server says the game is finished, clear stale local credentials so
  // that hitting "Accueil" then coming back doesn't show the user as seated.
  useEffect(() => {
    if (game?.status === "finished" && creds && game.winner) {
      // keep creds; just used for the "you" highlight in the final scoreboard
    }
  }, [game, creds]);

  // On the playing→finished transition, refetch ratings as a safety
  // net in case the WS "rated" event was lost (rare, but the live
  // path is fire-and-forget so we don't depend on it). If the server
  // hasn't applied yet we'll get applied:false now and the WS
  // subscription will swap to applied:true a moment later.
  const isFinished = game?.status === "finished";
  useEffect(() => {
    if (!isFinished) return;
    let cancelled = false;
    api
      .getGameRatings(id)
      .then((gr) => {
        if (!cancelled) setRatings(gr);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [id, isFinished]);

  async function handleJoin(joinAs: string | undefined) {
    setJoining(true);
    setError(null);
    try {
      const res = await api.joinGame(id, joinAs);
      saveCredentials(id, {
        token: res.token,
        seatIndex: res.seat.index,
        name: res.seat.name,
      });
      setOptimisticGame(res.game);
      return true;
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur inconnue");
      return false;
    } finally {
      setJoining(false);
    }
  }

  // Auto-join when a viewer lands on a still-waiting game without
  // creds. Authenticated users skip straight to /join (the server
  // resolves their name from the profile); anonymous users get a
  // modal asking for a one-time display name and join on submit.
  // Either way we never dangle a "Rejoindre" button — being a
  // spectator is reserved for games that are already in progress
  // or finished.
  //
  // autoJoinAttempted is a ref so the effect re-fires safely across
  // game state pushes without re-firing the actual join. If the auto
  // attempt fails (full game, all seats reserved for others, …) the
  // user falls back to spectator mode silently — they can refresh
  // to retry.
  const [nameModalOpen, setNameModalOpen] = useState(false);
  const autoJoinAttempted = useRef(false);
  useEffect(() => {
    if (!game) return;
    if (creds) return;
    if (game.status !== "waiting") return;
    if (autoJoinAttempted.current) return;
    if (joining) return;
    if (user) {
      autoJoinAttempted.current = true;
      void handleJoin(undefined);
    } else if (!nameModalOpen) {
      // Defer to the modal — once the user submits a name we'll
      // record the attempt below.
      setNameModalOpen(true);
    }
  }, [game, creds, user, joining, nameModalOpen]);

  // handleCancelMatchmaking: vacate a seat in a still-waiting game and go
  // home. Clear local creds eagerly so a stale WS state event doesn't put
  // us back in the seat we just vacated.
  async function handleCancelMatchmaking() {
    if (!creds) return;
    const token = creds.token;
    clearCredentials(id);
    setOptimisticGame(null);
    try {
      await api.leaveSeat(id, token);
    } catch {
      /* best-effort — server may already think we're gone */
    }
    navigate("/");
  }

  // Seat index of the local user in this finished game, derived from
  // saved credentials. null means "spectator" — the rematch controls
  // render in read-only mode for these viewers.
  const mySeatIndex = creds?.seatIndex ?? null;

  const { handleResign, handleOfferDraw, handleAcceptDraw, handleDeclineDraw } =
    useInPlayActions({
      gameId: id,
      creds,
      onGame: setOptimisticGame,
      onError: setError,
    });

  const {
    rematching,
    handleOfferRematch,
    handleDeclineRematch,
    handleGoToRematch,
  } = useRematchFlow({
    gameId: id,
    game,
    creds,
    onGame: setOptimisticGame,
    onError: setError,
  });

  async function openReplay() {
    setReplayLoading(true);
    setError(null);
    try {
      const r = await api.getReplay(id);
      setReplay(r);
      setReplayStep(r.steps.length);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur replay");
    } finally {
      setReplayLoading(false);
    }
  }

  function closeReplay() {
    setReplay(null);
    setReplayStep(0);
  }

  function handleLeave() {
    // Drop the local seat token and navigate home. We deliberately do
    // not stay on the game page after "Quitter" — the user's intent
    // is to leave the match, not to keep watching it. Anyone who
    // wants to reopen the game later can do so by URL.
    clearCredentials(id);
    setOptimisticGame(null);
    navigate("/");
  }

  // "Nouvelle partie" mirrors the visibility of the game that just
  // ended: a public/matchmade game funnels back into matchmaking
  // (via the dedicated /play/<mode> page), a private game spawns a
  // fresh empty private lobby at the engine's max seat count — same
  // shape as HomePage's "Créer une partie privée", so the host can
  // re-decide who plays (invite, add bot, leave empty) rather than
  // being locked into the previous game's seat count.
  // handleNewPrivateGame reuses the caller's seat name (held in creds)
  // so an anonymous host doesn't have to retype it.
  const [creatingNew, setCreatingNew] = useState(false);
  // Seat index currently being invited via the SeatInviteModal. -1
  // means the modal is closed; an integer value pins the modal to a
  // specific empty seat in the lobby.
  const [inviteSeatIdx, setInviteSeatIdx] = useState<number | null>(null);
  async function handleNewPrivateGame() {
    if (!game) return;
    setCreatingNew(true);
    setError(null);
    try {
      // 6 = engine's max seats; matches HomePage.PRIVATE_SEATS so the
      // two private-creation entrypoints behave identically.
      const res = await api.createGame(6, creds?.name);
      saveCredentials(res.game.id, {
        token: res.token,
        seatIndex: res.seat.index,
        name: res.seat.name,
      });
      navigate(`/game/${res.game.id}`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur création");
    } finally {
      setCreatingNew(false);
    }
  }
  const isPrivate = game?.visibility === "private";
  const playerCount = game?.seats.length ?? 2;
  // Private branch needs creds (we use the seat name for anon hosts;
  // for authed users the server falls back to the profile name, but
  // having creds at all means we know who's asking). Public/matchmade
  // branch needs auth — matchmaking 401s anonymous callers server-side.
  // Public branch just hands the user off to the dedicated matchmaking
  // page; the queue lifecycle lives there.
  const onNewGame =
    isPrivate && creds
      ? handleNewPrivateGame
      : !isPrivate && user
        ? () => navigate(playerCount > 2 ? "/play/multi" : "/play/1v1")
        : null;
  const newGameBusy = creatingNew;

  if (!game) {
    return (
      <Center>
        <p className="text-sm text-zinc-400">
          Connexion à la partie <code>{id}</code>…
        </p>
      </Center>
    );
  }

  // While replay is active, render the board from the replay's reconstructed
  // cells; clicks are disabled (we don't move from the past).
  const inReplay = replay !== null;
  const boardCells = inReplay
    ? cellsAtStep(replay.boardSide, replay.steps, replayStep)
    : game.cells;
  // In replay mode the highlight tracks the step cursor; in live play it
  // follows the server-reported last move (mirrors the chess.com "last
  // played" ring so a returning player can spot where the action just
  // happened). A waiting game with no moves has no lastMove → no ring.
  const boardHighlight = inReplay
    ? lastMoveAt(replay.steps, replayStep)
    : (game.lastMove ?? null);

  const seatsFree = game.seats.filter((s) => !s.occupied).length;
  const seatsOccupied = game.seats.length - seatsFree;

  // "Recherche d'adversaire" / "Salle d'attente multijoueur" — the
  // matchmaking-style screen. Renders whenever the caller is seated in a
  // public game still in waiting state. For 1v1, the second player's
  // arrival flips status to playing immediately (AllSeated path), so this
  // only ever shows briefly with 1/2 occupied. For multi, the room stays
  // waiting until the auto-promoter has at least 3 occupants AND the
  // threshold time for that occupancy has elapsed, so the user sees a
  // populated queue ("3/6 joueurs en attente") before play starts.
  const isSearching =
    game.status === "waiting" &&
    !!creds &&
    game.visibility === "public";
  if (isSearching) {
    return (
      <SearchingForOpponent
        maxPlayers={game.seats.length}
        seatsOccupied={seatsOccupied}
        onCancel={handleCancelMatchmaking}
      />
    );
  }

  // mobileBar pins the action block to the bottom of the viewport on
  // phones — fixed positioning + a translucent backdrop, with the iOS
  // safe-area inset added to py so the home-indicator strip doesn't
  // crowd the buttons. On lg+ the `lg:` resets drop it back into the
  // right aside as a regular block.
  const mobileBar =
    "fixed inset-x-0 bottom-0 z-30 border-t border-zinc-800 bg-zinc-950/95 p-2 pb-[max(0.5rem,env(safe-area-inset-bottom))] backdrop-blur lg:static lg:border-0 lg:bg-transparent lg:p-0 lg:backdrop-blur-none";

  return (
    // pb-24 lg:pb-4 leaves room for the fixed mobile action bar so the
    // chat / quitter link aren't hidden under it. On desktop the bar is
    // inline, no extra room needed.
    <div className="mx-auto max-w-[88rem] p-2 pb-24 lg:p-4 lg:pb-4">
      <header className="flex items-center justify-between">
        <Link
          to="/"
          className="flex items-center gap-2 text-lg font-semibold text-zinc-100 transition hover:text-amber-400"
        >
          <span
            aria-hidden
            className="inline-block h-4 w-4 rounded-sm bg-amber-400"
          />
          Gemline
        </Link>
        <div className="flex items-center gap-3">
          {!creds && game.status !== "waiting" && (
            <span
              className="rounded-full border border-zinc-700 bg-zinc-900/60 px-2 py-0.5 text-[11px] text-zinc-400"
              title="Tu observes cette partie sans y prendre part"
            >
              Spectateur
            </span>
          )}
          {/* Mobile-only: the inline Objectives panel below the board
             takes ~150 px, vertical real estate we can ill afford on a
             phone. Render a "?" button here that pops the same content
             as a modal; desktop keeps the inline version (right rail
             has room). */}
          <div className="lg:hidden">
            <ObjectivesPopover thresholds={game.thresholds} />
          </div>
          <ConnStatus status={wsStatus} attempt={wsAttempt} />
          <UserNav />
        </div>
      </header>

      {/*
        Layout:
          mobile portrait (default)            — flex-col, DOM order:
            scoreboard → board → right rail.
          mobile landscape (max-h:500px)       — 2-col grid: board on the
            left (row-span-2 to fill the viewport height), scoreboard +
            right-rail stack on the right. The geometry is what makes a
            phone in landscape feel cramped — that's why the height
            check triggers it, regardless of orientation prop.
          desktop (lg)                         — three columns:
            seats | board | conditions+chat.
        Each side rail is fixed-width; the board takes the remaining 1fr.
      */}
      <div className="mt-3 flex flex-col gap-3 [@media(max-height:500px)]:mt-2 [@media(max-height:500px)]:grid [@media(max-height:500px)]:grid-cols-[auto_minmax(0,1fr)] [@media(max-height:500px)]:items-start [@media(max-height:500px)]:gap-3 lg:mt-4 lg:grid lg:grid-cols-[16rem_minmax(0,1fr)_20rem] lg:items-start lg:gap-4">
        <aside className="flex flex-col gap-3 [@media(max-height:500px)]:col-start-2 lg:col-start-1">
          <Scoreboard
            game={game}
            mySeatIndex={creds?.seatIndex ?? null}
            myUserId={user?.id ?? null}
            presence={presence}
            ratings={ratings}
            onAddBot={
              game.status === "waiting" &&
              game.visibility === "private" &&
              !!creds
                ? async (seatIndex) => {
                    try {
                      const g = await api.addBot(id, seatIndex);
                      setOptimisticGame(g);
                    } catch (err) {
                      setError(err instanceof ApiError ? err.message : "Erreur bot");
                    }
                  }
                : undefined
            }
            onRemoveBot={
              game.status === "waiting" &&
              game.visibility === "private" &&
              !!creds
                ? async (seatIndex) => {
                    try {
                      const g = await api.removeBot(id, seatIndex);
                      setOptimisticGame(g);
                    } catch (err) {
                      setError(err instanceof ApiError ? err.message : "Erreur bot");
                    }
                  }
                : undefined
            }
            onInviteSeat={
              game.status === "waiting" &&
              game.visibility === "private" &&
              !!creds
                ? (seatIndex) => setInviteSeatIdx(seatIndex)
                : undefined
            }
            onCancelInvite={
              game.status === "waiting" &&
              game.visibility === "private" &&
              !!creds
                ? async (seatIndex) => {
                    try {
                      const g = await api.cancelSeatInvite(id, seatIndex);
                      setOptimisticGame(g);
                    } catch (err) {
                      setError(err instanceof ApiError ? err.message : "Erreur invitation");
                    }
                  }
                : undefined
            }
            onAcceptInvite={
              // Invitee accepting their own reservation — goes through
              // the standard join path; pickSeatForUser routes to the
              // reserved seat. Only meaningful while waiting + I'm not
              // already seated.
              game.status === "waiting" && !creds
                ? () => handleJoin(undefined)
                : undefined
            }
            onDeclineInvite={
              game.status === "waiting" && !creds && !!user
                ? async (seatIndex) => {
                    setError(null);
                    try {
                      const g = await api.declineSeatInvite(id, seatIndex);
                      setOptimisticGame(g);
                      navigate("/");
                    } catch (err) {
                      setError(err instanceof ApiError ? err.message : "Erreur invitation");
                    }
                  }
                : undefined
            }
          />

          {/* JoinPanel is gone — auto-join handles authed users and the
             AnonymousJoinModal handles anonymous ones. A viewer who
             can't get a seat (full game, no reserved seat for them)
             stays here as a spectator without seeing any "Rejoindre"
             affordance, since the server already refused. */}

          {game.status === "waiting" &&
            game.visibility === "private" &&
            creds &&
            creds.seatIndex === 0 && (
              // Host-only: the creator (seat 0) is the single source of
              // "start now" decisions. Guests just wait; the server
              // enforces the same rule with ErrNotHost, this guard is
              // for affordance — don't dangle a button that 403s.
              <StartButton
                game={game}
                onStart={async () => {
                  if (!creds) return;
                  try {
                    const g = await api.startGame(id, creds.token);
                    setOptimisticGame(g);
                  } catch (err) {
                    setError(err instanceof ApiError ? err.message : "Erreur start");
                  }
                }}
              />
            )}

          {game.status === "waiting" && (
            <ShareCard id={id} />
          )}
        </aside>

        <main className="[@media(max-height:500px)]:col-start-1 [@media(max-height:500px)]:row-span-2 lg:col-start-2">
          {/*
            Mobile portrait: drop the chrome (no border, near-zero padding)
            so the board itself dominates the viewport — every wasted px
            around the SVG is a px the cells lose, and the cells were
            already painful to tap at 14 px.
            Mobile landscape: same chrome strip, but fill the viewport
            height instead of using aspect-square — a phone in landscape is
            wider than tall, so a square container would overflow.
            Desktop: keep the rounded, padded card.
          */}
          <div className="aspect-square w-full bg-zinc-950/60 p-0.5 [@media(max-height:500px)]:aspect-square [@media(max-height:500px)]:h-[calc(100dvh-6rem)] [@media(max-height:500px)]:w-auto lg:aspect-auto lg:h-[min(80vh,calc(100vw-40rem))] lg:w-full lg:rounded-xl lg:border lg:border-zinc-800 lg:p-3">
            <Board
              side={inReplay ? replay.boardSide : game.boardSide}
              cells={boardCells}
              onPlay={inReplay ? undefined : onPlay}
              disabled={inReplay || !isMyTurn || game.status !== "playing"}
              highlight={boardHighlight}
              ghosts={inReplay ? undefined : ghosts}
              // Used by the Board's tap-to-confirm flow on coarse pointers
              // to paint the preview ghost in the local player's colour.
              // Undefined for spectators / replay viewers, which is fine —
              // they don't get the preview either.
              playerColor={
                creds && !inReplay
                  ? game.seats[creds.seatIndex]?.color
                  : undefined
              }
            />
          </div>
        </main>

        <aside className="flex flex-col gap-3 [@media(max-height:500px)]:col-start-2 lg:col-start-3">
          {/* Hidden on phones — same content is reachable via the "?"
             button in the header (ObjectivesPopover above). */}
          <div className="hidden lg:block">
            <Objectives thresholds={game.thresholds} />
          </div>

          {/*
            mobileBar groups the chrome that turns the action set into a
            fixed bottom bar on phones and a regular sidebar block on
            desktop. Same element either way — the lg: modifiers wipe the
            position / border / backdrop so it slots into the right aside.
            Combined with the page wrapper's pb-24 it never overlaps real
            content underneath, and the safe-area inset clears the iOS
            home-indicator strip.
          */}
          {game.status === "playing" && creds && (
            <div className={mobileBar}>
              <DrawOfferAndActions
                game={game}
                mySeatIndex={creds.seatIndex}
                onOfferDraw={handleOfferDraw}
                onAcceptDraw={handleAcceptDraw}
                onDeclineDraw={handleDeclineDraw}
                onResign={handleResign}
              />
            </div>
          )}

          {game.status === "finished" && (
            // End-of-game action block. Always visible (modal or not)
            // so the player has direct access to "what's next" without
            // having to re-open the modal. RematchControls renders the
            // chess.com-style state machine (propose / waiting / accept
            // or decline / go to rematch). The Elo deltas live in the
            // left Scoreboard — no "Revoir le résultat" needed here.
            <div className={`${mobileBar} space-y-2`}>
              {onNewGame && (
                <button
                  type="button"
                  onClick={onNewGame}
                  disabled={newGameBusy}
                  className="w-full rounded-md bg-amber-400 px-3 py-2 text-sm font-medium text-zinc-950 transition hover:bg-amber-300 disabled:opacity-50"
                >
                  {creatingNew ? "Création…" : "Nouvelle partie"}
                </button>
              )}
              <RematchControls
                game={game}
                mySeatIndex={mySeatIndex}
                busy={rematching}
                onOffer={handleOfferRematch}
                onDecline={handleDeclineRematch}
                onGoToRematch={handleGoToRematch}
              />
              <button
                type="button"
                onClick={handleLeave}
                className="w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm text-zinc-100 transition hover:border-zinc-500"
              >
                Quitter
              </button>
            </div>
          )}

          {inReplay ? (
            <ReplayControls
              step={replayStep}
              total={replay.steps.length}
              onChange={setReplayStep}
              onExit={closeReplay}
            />
          ) : (
            game.moveCount > 0 && (
              <button
                onClick={openReplay}
                disabled={replayLoading}
                className="rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm text-zinc-100 transition hover:border-zinc-500 disabled:opacity-50"
              >
                {replayLoading ? "Chargement…" : "Revoir la partie"}
              </button>
            )
          )}

          <ChatPanel gameId={id} playerToken={creds?.token ?? null} />

          {creds && (
            <button
              onClick={handleLeave}
              className="text-xs text-zinc-500 hover:text-zinc-300"
            >
              Quitter la partie (efface mon token local)
            </button>
          )}

          {error && (
            <p className="rounded-md border border-red-900/50 bg-red-950/30 p-3 text-sm text-red-300">
              {error}
            </p>
          )}
        </aside>
      </div>

      {game.status === "finished" && !endModalDismissed && (
        <GameEndModal
          game={game}
          ratings={ratings}
          mySeatIndex={mySeatIndex}
          rematching={rematching}
          newGameBusy={newGameBusy}
          newGameBusyLabel={creatingNew ? "Création…" : null}
          onOfferRematch={handleOfferRematch}
          onDeclineRematch={handleDeclineRematch}
          onGoToRematch={handleGoToRematch}
          onNewGame={onNewGame}
          onClose={() => setEndModalDismissed(true)}
          onLeave={handleLeave}
        />
      )}

      {inviteSeatIdx !== null && (
        <SeatInviteModal
          gameId={id}
          seatIndex={inviteSeatIdx}
          onInvited={(g) => setOptimisticGame(g)}
          onClose={() => setInviteSeatIdx(null)}
        />
      )}

      {nameModalOpen && (
        <AnonymousJoinModal
          seatsFree={seatsFree}
          initialName={name}
          submitting={joining}
          onSubmit={async (asName) => {
            autoJoinAttempted.current = true;
            setName(asName);
            const ok = await handleJoin(asName);
            // Only close on success. On failure we keep the modal so
            // the user can correct their name or back out; the error
            // message already surfaces under the input via the
            // shared `error` state which the modal mirrors.
            if (ok) setNameModalOpen(false);
          }}
        />
      )}
    </div>
  );
}

function Center({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-full items-center justify-center p-6">{children}</div>
  );
}
