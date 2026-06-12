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
import { ConnStatus } from "../components/ConnStatus";
import { DesktopGameAside } from "../components/DesktopGameAside";
import { DesktopGameRail } from "../components/DesktopGameRail";
import {
  GameBottomBar,
  type BottomBarMenuItem,
} from "../components/GameBottomBar";
import { GameEndModal } from "../components/GameEndModal";
import { RulesOverlay } from "../components/RulesOverlay";
import { PlayerStrip } from "../components/PlayerStrip";
import { SearchingForOpponent } from "../components/SearchingForOpponent";
import { SeatInviteModal } from "../components/SeatInviteModal";
import { ShareCard } from "../components/ShareCard";
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
  // Server's response to the caller's own mutations, merged with liveGame
  // via mergeGameSnapshot (see lib/gameSnapshot.ts for the tie-breaking rule).
  const [optimisticGame, setOptimisticGame] = useState<Game | null>(null);
  const [name, setName] = useState("");
  const [joining, setJoining] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [replay, setReplay] = useState<Replay | null>(null);
  const [replayStep, setReplayStep] = useState(0);
  const [, setReplayLoading] = useState(false);

  // Per-seat presence: true = online, false = nobody on the seat,
  // undefined = no presence event yet (default to optimistic online).
  const [presence, setPresence] = useState<Record<number, boolean>>({});

  // HTTP fetch on mount seeds this; the WS "rated" event overwrites once
  // the server applies deltas (both paths share the same shape).
  const [ratings, setRatings] = useState<GameRatings | null>(null);
  const [endModalDismissed, setEndModalDismissed] = useState(false);

  const [chatOpen, setChatOpen] = useState(false);
  const [rulesOpen, setRulesOpen] = useState(false);

  // Stones captured by the most recent move, kept briefly for the fade-out.
  // Unique keys avoid reusing a dying ghost when a later capture lands on
  // the same cell.
  const [ghosts, setGhosts] = useState<
    Array<{ q: number; r: number; color: Color; key: string }>
  >([]);

  const game = useMemo(
    () => mergeGameSnapshot(liveGame, optimisticGame),
    [liveGame, optimisticGame],
  );

  // Reactive to out-of-band writes (e.g. the seat-resolution effect below
  // saving creds once they're pulled).
  const creds = useCredentials(id);

  // Push our seat token so the server marks us online and cancels any
  // disconnect-grace timer.
  useEffect(() => {
    const socket = getSocket(id);
    socket.setHelloToken(creds?.token ?? null);
    return () => {
      socket.setHelloToken(null);
    };
  }, [id, creds?.token]);

  useEffect(() => {
    return acquirePresenceStream(id, (seatIndex, online) => {
      setPresence((prev) => ({ ...prev, [seatIndex]: online }));
    });
  }, [id]);

  // Initial ratings fetch; the "rated" subscription and finished-transition
  // refetch below cover the live case and missed-event resync.
  useEffect(() => {
    let cancelled = false;
    api
      .getGameRatings(id)
      .then((gr) => {
        if (!cancelled) setRatings(gr);
      })
      .catch(() => {
        // 404/transient: UI degrades to "no Elo info" (ratings stays null).
      });
    return () => {
      cancelled = true;
    };
  }, [id]);

  // Live path for the modal's delta section: server emits "rated" right
  // after ApplyRatedGame commits.
  useEffect(() => {
    return acquireRatedStream(id, (gr) => {
      setRatings(gr);
    });
  }, [id]);

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
      // No-op on iOS Safari and desktop without a haptic device (see lib/haptics).
      hapticCapture();
      const keys = new Set(added.map((g) => g.key));
      window.setTimeout(() => {
        setGhosts((prev) => prev.filter((g) => !keys.has(g.key)));
      }, 600);
    });
  }, [id]);

  // Buzz once on the playing → finished transition. The ref guards against
  // a fresh mount on an already-finished game buzzing on every page load.
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

  // Tap-to-confirm for coarse pointers (touch/stylus): renders a
  // Confirmer/Annuler banner outside the board SVG so the buttons don't
  // zoom with the pinch wrapper and stay thumb-reachable. Detected once
  // at mount — devices don't switch input modes mid-game.
  const isCoarsePointer = useMemo(
    () =>
      typeof window !== "undefined" &&
      window.matchMedia("(pointer: coarse)").matches,
    [],
  );
  const [pendingCell, setPendingCell] = useState<{ q: number; r: number } | null>(
    null,
  );
  // Derived-state reset of `pendingCell`: any shift in moveCount or
  // playableNow (move landed, turn flipped, replay opened, game ended)
  // clears a stale preview without an effect. Declared above the early
  // returns to keep hook order stable.
  const playableNow =
    isMyTurn && !!game && game.status === "playing" && replay === null;
  const moveCount = game?.moveCount ?? 0;
  const [prevMoveCount, setPrevMoveCount] = useState(moveCount);
  const [prevPlayableNow, setPrevPlayableNow] = useState(playableNow);
  if (prevMoveCount !== moveCount || prevPlayableNow !== playableNow) {
    setPrevMoveCount(moveCount);
    setPrevPlayableNow(playableNow);
    if (pendingCell !== null) setPendingCell(null);
  }

  useEffect(() => {
    if (game?.status === "finished" && creds && game.winner) {
      // Keep creds for the "you" highlight in the final scoreboard.
    }
  }, [game, creds]);

  // Refetch ratings on the playing→finished transition in case the WS
  // "rated" event was lost. applied:false now, the WS subscription swaps
  // to applied:true a moment later.
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

  // Auto-join a still-waiting game without creds: authed users join
  // directly, anonymous users get a name modal first. Spectating is
  // reserved for in-progress/finished games. autoJoinAttempted is a ref
  // so the effect re-fires across state pushes without re-joining; a
  // failed attempt falls back to spectator silently.
  const ownedSeatIndex =
    user && game
      ? (game.seats.find((s) => s.occupied && s.userId === user.id)?.index ??
        null)
      : null;

  // A pre-seated authed user pulls their seat token by JWT — the single creds
  // channel. Reset the guard on failure so the next state push retries.
  const resolveAttempted = useRef(false);
  useEffect(() => {
    if (creds || ownedSeatIndex === null) return;
    if (resolveAttempted.current) return;
    resolveAttempted.current = true;
    void (async () => {
      try {
        const res = await api.resolveSeat(id);
        saveCredentials(id, {
          token: res.token,
          seatIndex: res.seat.index,
          name: res.seat.name,
        });
      } catch {
        resolveAttempted.current = false;
      }
    })();
  }, [creds, ownedSeatIndex, id]);

  // Auto-join a still-waiting game for someone who doesn't already own a seat:
  // authed users join directly, anonymous users get a name modal first.
  const [nameModalOpen, setNameModalOpen] = useState(false);
  const autoJoinAttempted = useRef(false);
  useEffect(() => {
    if (!game) return;
    if (creds || ownedSeatIndex !== null) return;
    if (game.status !== "waiting") return;
    if (autoJoinAttempted.current) return;
    if (joining) return;
    if (user) {
      autoJoinAttempted.current = true;
      // The join is an intended side-effect, not derived state.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      void handleJoin(undefined);
    } else if (!nameModalOpen) {
      setNameModalOpen(true);
    }
    // handleJoin is recreated each render; listing it would re-fire the
    // effect every render. We only want auto-join once per mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [game, creds, ownedSeatIndex, user, joining, nameModalOpen]);

  // Vacate a seat in a still-waiting game and go home. Clear creds eagerly
  // so a stale WS state event doesn't re-seat us.
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

  // null = spectator (rematch controls render read-only).
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
    clearCredentials(id);
    setOptimisticGame(null);
    navigate("/");
  }

  // "Nouvelle partie" mirrors the ended game's visibility: public funnels
  // back into matchmaking, private spawns a fresh empty private lobby so
  // the host can re-decide who plays. handleNewPrivateGame reuses the
  // caller's seat name so an anonymous host doesn't retype it.
  const [creatingNew, setCreatingNew] = useState(false);
  const [inviteSeatIdx, setInviteSeatIdx] = useState<number | null>(null);
  async function handleNewPrivateGame() {
    if (!game) return;
    setCreatingNew(true);
    setError(null);
    try {
      // 6 = engine's max seats; matches HomePage.PRIVATE_SEATS.
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
  // Private branch needs creds; public/matchmade branch needs auth
  // (matchmaking 401s anonymous callers server-side).
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

  // In replay, render reconstructed cells with clicks disabled.
  const inReplay = replay !== null;
  const boardCells = inReplay
    ? cellsAtStep(replay.boardSide, replay.steps, replayStep)
    : game.cells;
  // Highlight tracks the step cursor in replay, the last move in live play.
  const boardHighlight = inReplay
    ? lastMoveAt(replay.steps, replayStep)
    : (game.lastMove ?? null);

  // Defined after the early returns because they close over `inReplay` /
  // final `game` / `boardDisabled`.
  const boardDisabled = inReplay || !isMyTurn || game.status !== "playing";
  const handleCellTap = (q: number, r: number) => {
    // Mouse/desktop or disabled board: commit directly. The preview only
    // earns its keep on touch where mis-taps on a tiny hitbox are the risk.
    if (!isCoarsePointer || boardDisabled) {
      void onPlay(q, r);
      return;
    }
    // Re-tap the armed cell → commit; different cell → move the preview.
    if (pendingCell && pendingCell.q === q && pendingCell.r === r) {
      setPendingCell(null);
      void onPlay(q, r);
    } else {
      setPendingCell({ q, r });
    }
  };
  const handleBoardTap = () => {
    if (pendingCell !== null) setPendingCell(null);
  };
  const handleConfirmPending = () => {
    if (!pendingCell) return;
    const { q, r } = pendingCell;
    setPendingCell(null);
    void onPlay(q, r);
  };
  const handleCancelPending = () => {
    setPendingCell(null);
  };

  const seatsFree = game.seats.filter((s) => !s.occupied).length;
  const seatsOccupied = game.seats.length - seatsFree;

  // Matchmaking-style screen: caller seated in a public game still waiting.
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

  // Lobby-only seat callbacks for PlayerStrip's inline +Inviter / +Bot
  // affordances; undefined outside (waiting + private + seated).
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

  // Kebab menu items, ordered primary-first, destructive-last.
  const menuItems: BottomBarMenuItem[] = [];
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
  menuItems.push({
    label: "Règles de la partie",
    onClick: () => setRulesOpen(true),
  });
  if (creds) {
    menuItems.push({
      label: "Quitter la partie",
      variant: "danger",
      onClick: handleLeave,
    });
  }

  // Draw-offer banner above the bottom bar — kept out of the kebab so the
  // accept/decline decision is one tap away.
  const drawOfferBy = game.drawOfferBy ?? -1;
  const showDrawOfferBanner =
    game.status === "playing" &&
    creds !== null &&
    drawOfferBy >= 0 &&
    drawOfferBy !== creds.seatIndex;

  return (
    // Single-viewport layout (h-dvh + overflow-hidden): the board's
    // pinch-pan wrapper competes with page scroll on phones. pb-24
    // reserves room for the fixed mobile GameBottomBar (dropped on desktop).
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

      {/* Mobile: flex-col (strip on top, board flex-1). Desktop (lg+):
          3-col grid (scoreboard | board | actions). Per-layout elements
          use lg:hidden / hidden lg:flex. */}
      <div className="mt-2 flex min-h-0 flex-1 flex-col gap-2 lg:mt-4 lg:grid lg:grid-cols-[16rem_minmax(0,1fr)_20rem] lg:grid-rows-[minmax(0,1fr)] lg:gap-4">
        <DesktopGameAside
          game={game}
          mySeatIndex={creds?.seatIndex ?? null}
          myUserId={user?.id ?? null}
          presence={presence}
          ratings={ratings}
          seatCallbacks={stripCallbacks}
          gameId={id}
          onStart={
            game.status === "waiting" &&
            game.visibility === "private" &&
            creds &&
            creds.seatIndex === 0
              ? async () => {
                  if (!creds) return;
                  try {
                    const g = await api.startGame(id, creds.token);
                    setOptimisticGame(g);
                  } catch (err) {
                    setError(
                      err instanceof ApiError ? err.message : "Erreur start",
                    );
                  }
                }
              : undefined
          }
        />

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
          {/* min(100cqw,100cqh) keeps the board square, filling whichever
              axis of <main> is more constraining. */}
          <div className="aspect-square w-[min(100cqw,100cqh)] bg-zinc-950/60 p-0.5 lg:rounded-xl lg:border lg:border-zinc-800 lg:p-3">
            <Board
              side={inReplay ? replay.boardSide : game.boardSide}
              cells={boardCells}
              onCellTap={inReplay ? undefined : handleCellTap}
              onBoardTap={inReplay ? undefined : handleBoardTap}
              pendingCell={pendingCell}
              disabled={boardDisabled}
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

        <DesktopGameRail
          game={game}
          gameId={id}
          mySeatIndex={mySeatIndex}
          playerToken={creds?.token ?? null}
          onOfferDraw={handleOfferDraw}
          onAcceptDraw={handleAcceptDraw}
          onDeclineDraw={handleDeclineDraw}
          onResign={handleResign}
          onNewGame={onNewGame}
          newGameBusy={newGameBusy}
          creatingNew={creatingNew}
          onOfferRematch={handleOfferRematch}
          onDeclineRematch={handleDeclineRematch}
          onGoToRematch={handleGoToRematch}
          rematching={rematching}
          totalMoves={game.moveCount}
          step={inReplay ? replayStep : game.moveCount}
          inReplay={inReplay}
          onStep={setReplayStep}
          openReplay={() => void openReplay()}
          exitReplay={closeReplay}
          onLeave={handleLeave}
          error={error}
        />
      </div>

      {/* Mobile error toast, fixed above the bottom bar (no-scroll layout). */}
      {error && (
        <button
          type="button"
          onClick={() => setError(null)}
          className="fixed inset-x-2 bottom-20 z-30 rounded-md border border-red-900/50 bg-red-950/95 p-3 text-left text-sm text-red-200 shadow-lg backdrop-blur lg:hidden"
        >
          {error}
        </button>
      )}

      {/* Tap-to-confirm banner, shown while a preview is armed. */}
      {pendingCell && (
        <div className="fixed inset-x-2 bottom-[calc(3.5rem+env(safe-area-inset-bottom))] z-40 flex items-center gap-2 rounded-md border border-amber-400/40 bg-zinc-950/95 p-2 shadow-lg backdrop-blur lg:inset-x-auto lg:right-4 lg:bottom-4 lg:w-auto">
          <span className="flex-1 px-1 text-sm text-amber-100 lg:flex-none">
            Poser ta gemme ici&nbsp;?
          </span>
          <button
            type="button"
            onClick={handleCancelPending}
            className="rounded-md border border-zinc-700 px-3 py-1.5 text-sm text-zinc-300 transition hover:border-zinc-500 hover:text-zinc-100"
          >
            Annuler
          </button>
          <button
            type="button"
            onClick={handleConfirmPending}
            className="rounded-md bg-amber-400 px-3 py-1.5 text-sm font-medium text-zinc-950 transition hover:bg-amber-300"
          >
            Confirmer
          </button>
        </div>
      )}

      {/* Mobile-only fixed-bottom toolbar (desktop uses the right rail). */}
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
