import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, ApiError } from "../api/client";
import { useAuth } from "../auth/useAuth";
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
import { ChatDrawer } from "../components/ChatDrawer";
import { ChatPanel } from "../components/ChatPanel";
import { ConnStatus } from "../components/ConnStatus";
import { DrawOfferAndActions } from "../components/DrawOfferAndActions";
import {
  GameBottomBar,
  type BottomBarMenuItem,
} from "../components/GameBottomBar";
import { GameEndModal } from "../components/GameEndModal";
import { Objectives } from "../components/Objectives";
import { RulesOverlay } from "../components/RulesOverlay";
import { PlayerStrip } from "../components/PlayerStrip";
import { RematchControls } from "../components/RematchControls";
import { ReplayNav } from "../components/ReplayNav";
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
  const [, setReplayLoading] = useState(false);

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

  // Chat drawer open/closed (used on every breakpoint now — desktop's
  // inline chat panel is gone, the drawer is the only entry point).
  const [chatOpen, setChatOpen] = useState(false);
  // Rules overlay open/closed. Triggered from the bottom-bar kebab.
  const [rulesOpen, setRulesOpen] = useState(false);

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
      // handleJoin internally setStates joining / creds / error. That's
      // what we want — the join is a side-effect, not derived state —
      // and the lint rule's complaint about setState-in-effect is the
      // false-positive case it documents (effect responding to a
      // server state change, dispatching an async network call).
      // eslint-disable-next-line react-hooks/set-state-in-effect
      void handleJoin(undefined);
    } else if (!nameModalOpen) {
      // Defer to the modal — once the user submits a name we'll
      // record the attempt below.
      setNameModalOpen(true);
    }
    // handleJoin captures `id` via closure — it's stable enough for
    // this effect's purpose (auto-join exactly once per mount on the
    // current game). Listing it in deps would re-fire the effect on
    // every render since handleJoin is recreated each render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
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

  // Lobby-only seat callbacks pass through to PlayerStrip's inline
  // +Inviter / +Bot affordances. Each one is undefined outside the
  // (waiting + private + seated) trifecta so the strip just renders
  // the empty card without action chrome.
  const isPrivateLobby =
    game.status === "waiting" && game.visibility === "private" && !!creds;
  const stripCallbacks = {
    onAddBot: isPrivateLobby
      ? async (seatIndex: number) => {
          try {
            const g = await api.addBot(id, seatIndex);
            setOptimisticGame(g);
          } catch (err) {
            setError(err instanceof ApiError ? err.message : "Erreur bot");
          }
        }
      : undefined,
    onRemoveBot: isPrivateLobby
      ? async (seatIndex: number) => {
          try {
            const g = await api.removeBot(id, seatIndex);
            setOptimisticGame(g);
          } catch (err) {
            setError(err instanceof ApiError ? err.message : "Erreur bot");
          }
        }
      : undefined,
    onInviteSeat: isPrivateLobby
      ? (seatIndex: number) => setInviteSeatIdx(seatIndex)
      : undefined,
    onCancelInvite: isPrivateLobby
      ? async (seatIndex: number) => {
          try {
            const g = await api.cancelSeatInvite(id, seatIndex);
            setOptimisticGame(g);
          } catch (err) {
            setError(err instanceof ApiError ? err.message : "Erreur invitation");
          }
        }
      : undefined,
    onAcceptInvite:
      game.status === "waiting" && !creds
        ? () => handleJoin(undefined)
        : undefined,
    onDeclineInvite:
      game.status === "waiting" && !creds && !!user
        ? async (seatIndex: number) => {
            setError(null);
            try {
              const g = await api.declineSeatInvite(id, seatIndex);
              setOptimisticGame(g);
              navigate("/");
            } catch (err) {
              setError(err instanceof ApiError ? err.message : "Erreur invitation");
            }
          }
        : undefined,
  };

  // Build the kebab menu items for the bottom bar from the current state.
  // The order is loosely "primary action first, destructive last".
  const menuItems: BottomBarMenuItem[] = [];
  // Lobby: host's Lancer la partie.
  if (
    game.status === "waiting" &&
    game.visibility === "private" &&
    creds &&
    creds.seatIndex === 0
  ) {
    const occupied = game.seats.filter((s) => s.occupied).length;
    menuItems.push({
      label: "Lancer la partie",
      variant: "primary",
      disabled: occupied < 2,
      onClick: async () => {
        if (!creds) return;
        try {
          const g = await api.startGame(id, creds.token);
          setOptimisticGame(g);
        } catch (err) {
          setError(err instanceof ApiError ? err.message : "Erreur start");
        }
      },
    });
  }
  // In-play: draw + resign.
  if (game.status === "playing" && creds) {
    const drawSupported = game.seats.length === 2;
    const offeredBy = game.drawOfferBy ?? -1;
    if (drawSupported && offeredBy < 0) {
      menuItems.push({ label: "Proposer un nul", onClick: handleOfferDraw });
    } else if (offeredBy === creds.seatIndex) {
      menuItems.push({
        label: "Retirer ma proposition de nul",
        onClick: handleDeclineDraw,
      });
    }
    menuItems.push({
      label: "Abandonner",
      variant: "danger",
      onClick: handleResign,
    });
  }
  // Finished: new game / rematch state machine.
  if (game.status === "finished") {
    if (onNewGame) {
      menuItems.push({
        label: creatingNew ? "Création…" : "Nouvelle partie",
        variant: "primary",
        disabled: newGameBusy,
        onClick: onNewGame,
      });
    }
    if (game.rematchGameId) {
      menuItems.push({
        label: "Aller à la revanche",
        onClick: handleGoToRematch,
      });
    } else if (creds) {
      const offer = game.rematchOffer;
      const iAccepted =
        offer?.acceptedSeats.includes(creds.seatIndex) ?? false;
      if (offer && iAccepted) {
        menuItems.push({
          label: "Annuler ma revanche",
          onClick: handleDeclineRematch,
          disabled: rematching,
        });
      } else if (offer) {
        menuItems.push({
          label: rematching ? "Envoi…" : "Accepter la revanche",
          variant: "primary",
          disabled: rematching,
          onClick: handleOfferRematch,
        });
        menuItems.push({
          label: "Refuser la revanche",
          variant: "danger",
          onClick: handleDeclineRematch,
        });
      } else {
        menuItems.push({
          label: rematching ? "Envoi…" : "Proposer une revanche",
          disabled: rematching,
          onClick: handleOfferRematch,
        });
      }
    }
  }
  // Always available — quick access to the rules card.
  menuItems.push({
    label: "Règles de la partie",
    onClick: () => setRulesOpen(true),
  });
  // Seated players can leave the game (clears the local token).
  if (creds) {
    menuItems.push({
      label: "Quitter la partie",
      variant: "danger",
      onClick: handleLeave,
    });
  }

  // Banner shown above the bottom bar when the opponent has just
  // offered a draw and the local player hasn't responded yet. Kept
  // out of the kebab menu so the decision is one tap away rather
  // than two and the affordance is unmissable.
  const drawOfferBy = game.drawOfferBy ?? -1;
  const showDrawOfferBanner =
    game.status === "playing" &&
    creds !== null &&
    drawOfferBy >= 0 &&
    drawOfferBy !== creds.seatIndex;

  return (
    // Single-viewport layout (h-dvh + overflow-hidden) at every
    // breakpoint — the board's pinch-pan wrapper captures touches and
    // competes with page scroll on phones, and on desktop the layout
    // already fits one viewport. pb-24 reserves room for the fixed
    // mobile GameBottomBar; on desktop the bar is gone and the action
    // chrome lives in the right rail, so we drop the bottom padding.
    <div className="mx-auto flex h-dvh max-w-[88rem] flex-col overflow-hidden px-2 pb-24 pt-2 lg:px-4 lg:pb-4 lg:pt-4">
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
        Two layouts share the same DOM with `lg:` gates:
          - Mobile (default): flex-col with PlayerStrip on top, board
            taking flex-1, optional ShareCard / draw banner under the
            board. The fixed GameBottomBar lives outside this column
            (below).
          - Desktop (lg+): 3-col grid — Scoreboard rail | board | info
            + actions rail. Same board renders in the centre cell.
        Elements that only make sense in one of the two paths use
        `lg:hidden` or `hidden lg:flex` to drop out of the other.
      */}
      <div className="mt-2 flex min-h-0 flex-1 flex-col gap-2 lg:mt-4 lg:grid lg:grid-cols-[16rem_minmax(0,1fr)_20rem] lg:grid-rows-[minmax(0,1fr)] lg:gap-4">
        {/* Desktop-only left rail: per-seat scoreboard + lobby actions.
           Same content as before the BoardFirst refactor. */}
        <aside className="hidden flex-col gap-3 lg:flex lg:col-start-1 lg:self-start">
          <Scoreboard
            game={game}
            mySeatIndex={creds?.seatIndex ?? null}
            myUserId={user?.id ?? null}
            presence={presence}
            ratings={ratings}
            onAddBot={stripCallbacks.onAddBot}
            onRemoveBot={stripCallbacks.onRemoveBot}
            onInviteSeat={stripCallbacks.onInviteSeat}
            onCancelInvite={stripCallbacks.onCancelInvite}
            onAcceptInvite={stripCallbacks.onAcceptInvite}
            onDeclineInvite={stripCallbacks.onDeclineInvite}
          />
          {game.status === "waiting" &&
            game.visibility === "private" &&
            creds &&
            creds.seatIndex === 0 && (
              <StartButton
                game={game}
                onStart={async () => {
                  if (!creds) return;
                  try {
                    const g = await api.startGame(id, creds.token);
                    setOptimisticGame(g);
                  } catch (err) {
                    setError(
                      err instanceof ApiError ? err.message : "Erreur start",
                    );
                  }
                }}
              />
            )}
          {game.status === "waiting" && <ShareCard id={id} />}
        </aside>

        {/* Mobile-only player strip on top. */}
        <div className="lg:hidden">
          <PlayerStrip
            game={game}
            mySeatIndex={creds?.seatIndex ?? null}
            myUserId={user?.id ?? null}
            presence={presence}
            ratings={ratings}
            {...stripCallbacks}
          />
        </div>

        <main className="@container flex min-h-0 flex-1 items-center justify-center lg:col-start-2">
          {/*
            w-[min(100cqw,100cqh)] reads container query units off <main>
            and picks the smaller of its width / height, so the board is
            a square that fills its slot regardless of which axis is
            constraining.
          */}
          <div className="aspect-square w-[min(100cqw,100cqh)] bg-zinc-950/60 p-0.5 lg:rounded-xl lg:border lg:border-zinc-800 lg:p-3">
            <Board
              side={inReplay ? replay.boardSide : game.boardSide}
              cells={boardCells}
              onPlay={inReplay ? undefined : onPlay}
              disabled={inReplay || !isMyTurn || game.status !== "playing"}
              highlight={boardHighlight}
              ghosts={inReplay ? undefined : ghosts}
              playerColor={
                creds && !inReplay
                  ? game.seats[creds.seatIndex]?.color
                  : undefined
              }
            />
          </div>
        </main>

        {/* Mobile-only below-board chrome — share card and draw banner. */}
        {game.status === "waiting" && game.visibility === "private" && (
          <div className="lg:hidden">
            <ShareCard id={id} />
          </div>
        )}
        {showDrawOfferBanner && creds && (
          <div className="rounded-md border border-amber-400/40 bg-amber-400/10 p-2 text-sm text-amber-100 lg:hidden">
            <p>
              🤝{" "}
              <span className="font-medium">
                {game.seats[drawOfferBy]?.name ?? "Adversaire"}
              </span>{" "}
              propose un nul.
            </p>
            <div className="mt-1 flex gap-2">
              <button
                type="button"
                onClick={handleAcceptDraw}
                className="flex-1 rounded-md bg-amber-400 px-3 py-1.5 text-xs font-medium text-zinc-950 transition hover:bg-amber-300"
              >
                Accepter
              </button>
              <button
                type="button"
                onClick={handleDeclineDraw}
                className="flex-1 rounded-md border border-amber-400/50 px-3 py-1.5 text-xs text-amber-100 transition hover:bg-amber-400/10"
              >
                Refuser
              </button>
            </div>
          </div>
        )}

        {/* Desktop-only right rail. */}
        <aside className="hidden flex-col gap-3 lg:flex lg:col-start-3 lg:self-start">
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

          {/* Replay nav — always visible when there are moves to step
             through. Tapping ◀ on the live board enters replay; the
             "live" chip exits. Same component as the mobile bottom-bar
             middle slot, just rendered inline here. */}
          {game.moveCount > 0 && (
            <div className="rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2">
              <ReplayNav
                totalMoves={game.moveCount}
                step={inReplay ? replayStep : game.moveCount}
                inReplay={inReplay}
                onStep={setReplayStep}
                openReplay={() => void openReplay()}
                exitReplay={closeReplay}
              />
            </div>
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

      {/* Mobile error toast — fixed above the bottom bar so it stays
         visible in the no-scroll layout. Tap to dismiss. Desktop
         renders the same message inline in the right rail. */}
      {error && (
        <button
          type="button"
          onClick={() => setError(null)}
          className="fixed inset-x-2 bottom-20 z-30 rounded-md border border-red-900/50 bg-red-950/95 p-3 text-left text-sm text-red-200 shadow-lg backdrop-blur lg:hidden"
        >
          {error}
        </button>
      )}

      {/* Mobile-only fixed-bottom toolbar. Desktop has the equivalent
         actions in the right rail and a permanent chat panel, so the
         bar isn't needed there. */}
      <div className="lg:hidden">
        <GameBottomBar
          totalMoves={game.moveCount}
          step={inReplay ? replayStep : game.moveCount}
          inReplay={inReplay}
          onStep={setReplayStep}
          openReplay={() => void openReplay()}
          exitReplay={closeReplay}
          onOpenChat={() => setChatOpen(true)}
          menuItems={menuItems}
        />
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
            if (ok) setNameModalOpen(false);
          }}
        />
      )}

      <ChatDrawer
        open={chatOpen}
        onClose={() => setChatOpen(false)}
        gameId={id}
        playerToken={creds?.token ?? null}
      />

      <RulesOverlay
        thresholds={game.thresholds}
        open={rulesOpen}
        onClose={() => setRulesOpen(false)}
      />
    </div>
  );
}

function Center({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-full items-center justify-center p-6">{children}</div>
  );
}
