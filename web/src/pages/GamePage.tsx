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
  type ConnStatus as WsConnStatus,
} from "../api/gameSocket";
import { useGameSocket } from "../api/ws";
import { Board } from "../components/Board";
import { ChatPanel } from "../components/ChatPanel";
import { GameEndModal } from "../components/GameEndModal";
import { SeatInviteModal } from "../components/SeatInviteModal";
import { Objectives } from "../components/Objectives";
import { RematchControls } from "../components/RematchControls";
import { ReplayControls } from "../components/ReplayControls";
import { Scoreboard } from "../components/Scoreboard";
import { UserNav } from "../components/UserNav";
import { clearCredentials, loadCredentials, saveCredentials } from "../lib/auth";
import { gemName } from "../lib/colors";
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
  const [localGame, setLocalGame] = useState<Game | null>(null);
  const [name, setName] = useState("");
  const [joining, setJoining] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [rematching, setRematching] = useState(false);

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

  // Pick the freshest snapshot. localGame only beats liveGame when it
  // has strictly more moves — i.e. when our optimistic mutation (our
  // own postMove) hasn't been confirmed by the WS state event yet.
  // On ties we MUST defer to liveGame: many server-side transitions
  // (waiting → playing on AllSeated, draw offers, seat invitations,
  // rematch state, etc.) change the DTO without bumping moveCount,
  // and a `>=` here would let a stale localGame mask those updates
  // until the user refreshed.
  const game = useMemo(() => {
    if (!localGame) return liveGame;
    if (!liveGame) return localGame;
    return localGame.moveCount > liveGame.moveCount ? localGame : liveGame;
  }, [liveGame, localGame]);

  const creds = useMemo(() => loadCredentials(id), [id, game]);

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
      const keys = new Set(added.map((g) => g.key));
      window.setTimeout(() => {
        setGhosts((prev) => prev.filter((g) => !keys.has(g.key)));
      }, 600);
    });
  }, [id]);

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
        setLocalGame(res.game);
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
      setLocalGame(res.game);
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
    setLocalGame(null);
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

  function handleGoToRematch() {
    if (!game?.rematchGameId) return;
    navigate(`/game/${game.rematchGameId}`);
  }

  // handleOfferRematch is the "Propose" / "Accept" call — the server
  // disambiguates by whether an offer is already pending. When this
  // call is the *last* acceptance, the response carries rematchGameId
  // and we navigate straight in (the other accepted players see the
  // link via the WS state event and click "Aller à la revanche").
  async function handleOfferRematch() {
    if (!creds) return;
    setRematching(true);
    setError(null);
    try {
      const g = await api.offerRematch(id, creds.token);
      setLocalGame(g);
      if (g.rematchGameId) {
        navigate(`/game/${g.rematchGameId}`);
      }
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur revanche");
    } finally {
      setRematching(false);
    }
  }

  async function handleDeclineRematch() {
    if (!creds) return;
    setRematching(true);
    setError(null);
    try {
      const g = await api.declineRematch(id, creds.token);
      setLocalGame(g);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur revanche");
    } finally {
      setRematching(false);
    }
  }

  // Auto-redirect both players to the rematch the moment it's created.
  // The acceptor who triggered the unanimous flip already navigates from
  // handleOfferRematch above; this effect handles the *other* accepters
  // who learn about the new game via the WS state event. We track the
  // last-seen rematchGameId via a ref so a fresh page load on a finished
  // game that already has a rematch doesn't kidnap the viewer — only a
  // genuine empty → set transition triggers the jump.
  const lastRematchIdRef = useRef<string | undefined>(undefined);
  const sawRematchRef = useRef(false);
  useEffect(() => {
    if (!game) return;
    const curr = game.rematchGameId;
    if (!sawRematchRef.current) {
      sawRematchRef.current = true;
      lastRematchIdRef.current = curr;
      return;
    }
    const prev = lastRematchIdRef.current;
    lastRematchIdRef.current = curr;
    if (curr && !prev && creds) {
      navigate(`/game/${curr}`);
    }
  }, [game, creds, navigate]);

  const handleResign = useCallback(async () => {
    if (!creds) return;
    if (!window.confirm("Abandonner la partie ?")) return;
    setError(null);
    try {
      const g = await api.resign(id, creds.token);
      setLocalGame(g);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur forfait");
    }
  }, [creds, id]);

  const handleOfferDraw = useCallback(async () => {
    if (!creds) return;
    setError(null);
    try {
      const g = await api.offerDraw(id, creds.token);
      setLocalGame(g);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur nul");
    }
  }, [creds, id]);

  const handleAcceptDraw = useCallback(async () => {
    if (!creds) return;
    setError(null);
    try {
      const g = await api.acceptDraw(id, creds.token);
      setLocalGame(g);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur nul");
    }
  }, [creds, id]);

  const handleDeclineDraw = useCallback(async () => {
    if (!creds) return;
    setError(null);
    try {
      const g = await api.declineDraw(id, creds.token);
      setLocalGame(g);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur nul");
    }
  }, [creds, id]);

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
    setLocalGame(null);
    navigate("/");
  }

  // "Nouvelle partie" mirrors the visibility of the game that just
  // ended: a public/matchmade game funnels back into matchmaking
  // (via the dedicated /play/<mode> page), a private game spawns a
  // fresh empty private game with the same player count.
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
      const res = await api.createGame(game.seats.length, creds?.name);
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

  return (
    <div className="mx-auto max-w-[88rem] p-3 lg:p-4">
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
          <ConnStatus status={wsStatus} attempt={wsAttempt} />
          <UserNav />
        </div>
      </header>

      {/*
        Layout:
          mobile (default) — flex-col, DOM order: scoreboard → board → right rail
          desktop (lg)     — three columns: seats | board | conditions+chat
        Each side rail is fixed-width; the board takes the remaining 1fr.
      */}
      <div className="mt-3 flex flex-col gap-3 lg:mt-4 lg:grid lg:grid-cols-[16rem_minmax(0,1fr)_20rem] lg:items-start lg:gap-4">
        <aside className="flex flex-col gap-3 lg:col-start-1">
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
                      setLocalGame(g);
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
                      setLocalGame(g);
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
                      setLocalGame(g);
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
                      setLocalGame(g);
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
                    setLocalGame(g);
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

        <main className="lg:col-start-2">
          <div className="aspect-square w-full rounded-xl border border-zinc-800 bg-zinc-950/60 p-3 lg:aspect-auto lg:h-[min(80vh,calc(100vw-40rem))]">
            <Board
              side={inReplay ? replay.boardSide : game.boardSide}
              cells={boardCells}
              onPlay={inReplay ? undefined : onPlay}
              disabled={inReplay || !isMyTurn || game.status !== "playing"}
              highlight={boardHighlight}
              ghosts={inReplay ? undefined : ghosts}
            />
          </div>
        </main>

        <aside className="flex flex-col gap-3 lg:col-start-3">
          <Objectives thresholds={game.thresholds} />

          {game.status === "playing" && creds && (
            <DrawOfferAndActions
              game={game}
              mySeatIndex={creds.seatIndex}
              onOfferDraw={handleOfferDraw}
              onAcceptDraw={handleAcceptDraw}
              onDeclineDraw={handleDeclineDraw}
              onResign={handleResign}
            />
          )}

          {game.status === "finished" && (
            // End-of-game action block. Always visible (modal or not)
            // so the player has direct access to "what's next" without
            // having to re-open the modal. RematchControls renders the
            // chess.com-style state machine (propose / waiting / accept
            // or decline / go to rematch). The Elo deltas live in the
            // left Scoreboard — no "Revoir le résultat" needed here.
            <div className="space-y-2">
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
          onInvited={(g) => setLocalGame(g)}
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

// AnonymousJoinModal is the one-time "what's your name?" prompt for
// anonymous visitors landing on a waiting game. Authenticated users
// auto-join silently with their profile name, so this is only ever
// seen by guests. Required because the server has no other way to
// identify an anon seat. The form is blocking (no backdrop close,
// no X) — the alternative is "click around an empty game you can't
// interact with", which is worse.
function AnonymousJoinModal({
  seatsFree,
  initialName,
  submitting,
  onSubmit,
}: {
  seatsFree: number;
  initialName: string;
  submitting: boolean;
  onSubmit: (name: string) => void | Promise<void>;
}) {
  const [name, setName] = useState(initialName);
  const trimmed = name.trim();
  const disabled = submitting || trimmed.length === 0;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (disabled) return;
          void onSubmit(trimmed);
        }}
        className="w-full max-w-sm space-y-3 rounded-2xl border border-zinc-800 bg-zinc-950 p-6 shadow-2xl"
      >
        <header>
          <h2 className="text-lg font-semibold text-zinc-100">
            Rejoindre la partie
          </h2>
          <p className="mt-1 text-xs text-zinc-400">
            {seatsFree > 0
              ? `${seatsFree} place${seatsFree > 1 ? "s" : ""} libre${seatsFree > 1 ? "s" : ""}. Choisis un pseudo.`
              : "Plus de place — tu pourras observer la partie."}
          </p>
        </header>
        <input
          autoFocus
          className="block w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-zinc-100 focus:border-amber-400 focus:outline-none"
          placeholder="Ton nom"
          value={name}
          maxLength={32}
          onChange={(e) => setName(e.target.value)}
          disabled={seatsFree === 0}
        />
        <button
          type="submit"
          disabled={disabled || seatsFree === 0}
          className="w-full rounded-md bg-amber-400 px-3 py-2 text-sm font-medium text-zinc-950 transition hover:bg-amber-300 disabled:opacity-50"
        >
          {submitting ? "…" : "Rejoindre"}
        </button>
        <p className="text-[11px] text-zinc-500">
          Ou{" "}
          <a href="/login" className="text-amber-400 hover:underline">
            connecte-toi
          </a>{" "}
          pour jouer sous ton nom de profil.
        </p>
      </form>
    </div>
  );
}

function ConnStatus({
  status,
  attempt,
}: {
  status: WsConnStatus;
  attempt: number;
}) {
  const meta = statusMeta(status, attempt);
  return (
    <span
      className="flex items-center gap-1.5 text-xs text-zinc-400"
      title={meta.title}
    >
      <span className={`inline-block h-2 w-2 rounded-full ${meta.dot}`} />
      {meta.label}
    </span>
  );
}

function statusMeta(
  status: WsConnStatus,
  attempt: number,
): { dot: string; label: string; title: string } {
  switch (status) {
    case "open":
      return { dot: "bg-emerald-500", label: "en ligne", title: "WebSocket connectée" };
    case "connecting":
      return {
        dot: "bg-amber-500 animate-pulse",
        label: "connexion…",
        title: "Ouverture de la WebSocket",
      };
    case "reconnecting":
      return {
        dot: "bg-amber-500 animate-pulse",
        label: `reconnexion (essai ${attempt})`,
        title: `Tentative ${attempt} de reconnexion à la WebSocket`,
      };
    case "offline":
      return {
        dot: "bg-red-500",
        label: "hors-ligne",
        title:
          "Échec après plusieurs tentatives — recharge la page ou vérifie la connexion",
      };
  }
}

// SearchingForOpponent is the chess.com-style waiting room for matchmade
// games. Renders before the game layout so the user sees a clean "queue"
// state instead of an empty board. For 1v1 it just spins; for multi it
// shows live progress (3/6 joueurs) so the user knows others are arriving.
function SearchingForOpponent({
  maxPlayers,
  seatsOccupied,
  onCancel,
}: {
  maxPlayers: number;
  seatsOccupied: number;
  onCancel: () => void;
}) {
  const isMulti = maxPlayers > 2;
  return (
    <div className="flex h-screen items-center justify-center bg-zinc-950 p-6">
      <div className="w-full max-w-sm space-y-6 rounded-xl border border-zinc-800 bg-zinc-900/60 p-6 text-center">
        <div
          aria-hidden
          className="mx-auto h-8 w-8 animate-spin rounded-full border-2 border-zinc-700 border-t-amber-400"
        />
        <div className="space-y-1">
          <h1 className="text-lg font-medium text-zinc-100">
            {isMulti
              ? "Salle d'attente multijoueur"
              : "Recherche d'un adversaire…"}
          </h1>
          {isMulti ? (
            <>
              <p className="text-2xl font-semibold text-amber-300">
                {seatsOccupied}/{maxPlayers}
              </p>
              <p className="text-sm text-zinc-400">
                La partie démarre dès que 3 joueurs ou plus sont là (plus tu
                attends, plus le seuil descend).
              </p>
            </>
          ) : (
            <p className="text-sm text-zinc-400">Partie 1 contre 1.</p>
          )}
        </div>
        <button
          type="button"
          onClick={onCancel}
          className="text-sm text-zinc-400 underline-offset-2 transition hover:text-zinc-200 hover:underline"
        >
          Annuler
        </button>
      </div>
    </div>
  );
}

function ShareCard({ id }: { id: string }) {
  const url = `${window.location.origin}/game/${id}`;
  return (
    <div className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-3 text-xs text-zinc-400">
      <div className="mb-1 font-medium text-zinc-300">Inviter</div>
      <input
        readOnly
        value={url}
        onFocus={(e) => e.currentTarget.select()}
        className="w-full rounded border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-300"
      />
    </div>
  );
}

function StartButton({ game, onStart }: { game: Game; onStart: () => void }) {
  const occupied = game.seats.filter((s) => s.occupied).length;
  const ready = occupied >= 2;
  return (
    <button
      type="button"
      onClick={onStart}
      disabled={!ready}
      className={
        "w-full rounded-xl border px-4 py-3 text-left transition disabled:cursor-not-allowed " +
        (ready
          ? "border-amber-400 bg-amber-400/10 text-amber-100 hover:bg-amber-400/20"
          : "border-zinc-800 bg-zinc-900/30 text-zinc-500")
      }
    >
      <div className="text-sm font-medium">
        {ready ? "Lancer la partie" : "Lancer la partie"}
      </div>
      <div className="mt-0.5 text-[11px]">
        {ready
          ? `${occupied} joueur${occupied > 1 ? "s" : ""} — les sièges vides resteront vides.`
          : "Au moins 2 sièges occupés (invite un joueur ou ajoute un bot)."}
      </div>
    </button>
  );
}

// DrawOfferAndActions renders the per-seat action area while a game is in
// play: forfait + nul-related buttons, plus the offer banner when one is
// pending. Multi-player games drop the draw controls entirely since draws
// are only supported in 2-player.
function DrawOfferAndActions({
  game,
  mySeatIndex,
  onOfferDraw,
  onAcceptDraw,
  onDeclineDraw,
  onResign,
}: {
  game: Game;
  mySeatIndex: number;
  onOfferDraw: () => void;
  onAcceptDraw: () => void;
  onDeclineDraw: () => void;
  onResign: () => void;
}) {
  const drawSupported = game.seats.length === 2;
  const offeredBy = game.drawOfferBy ?? -1;
  const offerPendingByMe = offeredBy === mySeatIndex;
  const offerPendingByThem = offeredBy >= 0 && !offerPendingByMe;

  return (
    <div className="space-y-2">
      {offerPendingByThem && (
        <div className="space-y-2 rounded-xl border border-amber-400/40 bg-amber-400/10 p-3 text-sm text-amber-100">
          <div>
            🤝 {gemName(game.seats[offeredBy]?.color ?? 0)} propose un nul.
          </div>
          <div className="flex gap-2">
            <button
              onClick={onAcceptDraw}
              className="flex-1 rounded-md bg-amber-400 px-3 py-1.5 text-xs font-medium text-zinc-950 transition hover:bg-amber-300"
            >
              Accepter
            </button>
            <button
              onClick={onDeclineDraw}
              className="flex-1 rounded-md border border-amber-400/50 px-3 py-1.5 text-xs text-amber-100 transition hover:bg-amber-400/10"
            >
              Refuser
            </button>
          </div>
        </div>
      )}

      {offerPendingByMe && (
        <div className="flex items-center justify-between rounded-md border border-zinc-700 bg-zinc-900/60 p-2 text-xs text-zinc-300">
          <span>En attente de l'adversaire pour le nul…</span>
          <button
            onClick={onDeclineDraw}
            className="text-zinc-400 underline-offset-2 hover:text-zinc-200 hover:underline"
          >
            Retirer
          </button>
        </div>
      )}

      <div className="flex gap-2">
        {drawSupported && offeredBy < 0 && (
          <button
            onClick={onOfferDraw}
            className="flex-1 rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-xs text-zinc-200 transition hover:border-zinc-500"
          >
            Proposer un nul
          </button>
        )}
        <button
          onClick={onResign}
          className="flex-1 rounded-md border border-red-900/50 bg-red-950/30 px-3 py-2 text-xs text-red-200 transition hover:border-red-700"
        >
          Abandonner
        </button>
      </div>
    </div>
  );
}
