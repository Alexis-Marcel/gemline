import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, ApiError } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import type { Color, Game, Replay, WinKind } from "../api/types";
import {
  acquireMoveStream,
  acquirePresenceStream,
  getSocket,
  type ConnStatus as WsConnStatus,
} from "../api/gameSocket";
import { useGameSocket } from "../api/ws";
import { Board } from "../components/Board";
import { ChatPanel } from "../components/ChatPanel";
import { Objectives } from "../components/Objectives";
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

  // Stones captured by the most recent move, kept around briefly so the
  // Board can animate them out. Each entry has a unique key so React doesn't
  // re-use a dying ghost when a subsequent capture lands on the same cell.
  const [ghosts, setGhosts] = useState<
    Array<{ q: number; r: number; color: Color; key: string }>
  >([]);

  // Prefer the most recent snapshot from either the WS stream or a local
  // mutation (e.g. our own move) so the UI never appears stale.
  const game = useMemo(() => {
    if (!localGame) return liveGame;
    if (!liveGame) return localGame;
    return localGame.moveCount >= liveGame.moveCount ? localGame : liveGame;
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
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur inconnue");
    } finally {
      setJoining(false);
    }
  }

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

  // If a rematch was created for this game (either by us or someone else),
  // the server exposes the linked ID on the snapshot. Offer a direct jump.
  const rematchLink = game?.rematchGameId ?? null;

  async function handleRematch() {
    setRematching(true);
    setError(null);
    try {
      const res = await api.rematch(id);
      navigate(`/game/${res.gameId}`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur revanche");
    } finally {
      setRematching(false);
    }
  }

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
    clearCredentials(id);
    setLocalGame(null);
    // Force a re-eval of creds by reloading the route — simplest.
    window.location.reload();
  }

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
  const boardHighlight = inReplay ? lastMoveAt(replay.steps, replayStep) : null;

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
            presence={presence}
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
          />

          {game.status === "waiting" && !creds && (
            <JoinPanel
              isAuthed={!!user}
              name={name}
              onChange={setName}
              onJoin={(asName) => handleJoin(asName)}
              disabled={joining}
              seatsFree={seatsFree}
            />
          )}

          {game.status === "waiting" &&
            game.visibility === "private" &&
            creds && (
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
            <>
              <Banner>
                🏆 {gemName(game.winner)} gagne par {winKindLabel(game.winKind)}
              </Banner>
              <button
                onClick={handleRematch}
                disabled={rematching}
                className="rounded-md bg-amber-400 px-3 py-2 text-sm font-medium text-zinc-950 transition hover:bg-amber-300 disabled:opacity-50"
              >
                {rematching
                  ? "Création…"
                  : rematchLink
                    ? "Aller à la revanche"
                    : "Revanche"}
              </button>
            </>
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
    </div>
  );
}

function Center({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-full items-center justify-center p-6">{children}</div>
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

// JoinPanel hides the legacy "type your name" form for authenticated users —
// the server pulls the display name from their profile, so a single
// "Rejoindre" button is enough. Anonymous users still get the name input,
// since the server has no way to identify them otherwise.
function JoinPanel({
  isAuthed,
  name,
  onChange,
  onJoin,
  disabled,
  seatsFree,
}: {
  isAuthed: boolean;
  name: string;
  onChange: (v: string) => void;
  onJoin: (name?: string) => void;
  disabled: boolean;
  seatsFree: number;
}) {
  if (seatsFree === 0) return null;
  if (isAuthed) {
    return (
      <button
        type="button"
        onClick={() => onJoin(undefined)}
        disabled={disabled}
        className="w-full rounded-xl border border-amber-400 bg-amber-400/10 px-3 py-3 text-left transition hover:bg-amber-400/20 disabled:opacity-50"
      >
        <div className="text-sm font-medium text-amber-100">
          {disabled ? "…" : "Rejoindre la partie"}
        </div>
        <div className="mt-0.5 text-xs text-zinc-400">
          Tu joues sous ton nom de profil.
        </div>
      </button>
    );
  }
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!name.trim()) return;
        onJoin(name.trim());
      }}
      className="space-y-2 rounded-xl border border-zinc-800 bg-zinc-900/40 p-3"
    >
      <h2 className="text-sm font-medium text-zinc-200">
        Rejoindre ({seatsFree} place{seatsFree > 1 ? "s" : ""} libre
        {seatsFree > 1 ? "s" : ""})
      </h2>
      <input
        autoFocus
        className="block w-full rounded-md border border-zinc-700 bg-zinc-950 px-3 py-2 text-zinc-100"
        placeholder="Ton nom"
        value={name}
        onChange={(e) => onChange(e.target.value)}
      />
      <button
        type="submit"
        disabled={disabled || !name.trim()}
        className="w-full rounded-md bg-amber-400 px-3 py-2 text-sm font-medium text-zinc-950 transition hover:bg-amber-300 disabled:opacity-50"
      >
        {disabled ? "..." : "Rejoindre"}
      </button>
    </form>
  );
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

function Banner({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-xl border border-amber-400/40 bg-amber-400/10 p-3 text-sm text-amber-200">
      {children}
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

function winKindLabel(k: WinKind): string {
  switch (k) {
    case 1:
      return "alignement de 6";
    case 2:
      return "alignements de 5";
    case 3:
      return "alignements de 4";
    case 4:
      return "captures";
    case 5:
      return "drapeau (temps écoulé)";
    case 6:
      return "forfait";
    case 7:
      return "nul d'accord parties";
    default:
      return "?";
  }
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
