import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, ApiError } from "../api/client";
import type { Game, WinKind } from "../api/types";
import type { ConnStatus as WsConnStatus } from "../api/gameSocket";
import { useGameSocket } from "../api/ws";
import { Board } from "../components/Board";
import { Objectives } from "../components/Objectives";
import { Scoreboard } from "../components/Scoreboard";
import { UserNav } from "../components/UserNav";
import { clearCredentials, loadCredentials, saveCredentials } from "../lib/auth";
import { gemName } from "../lib/colors";

export function GamePage() {
  const { id = "" } = useParams();
  const {
    game: liveGame,
    status: wsStatus,
    attempt: wsAttempt,
  } = useGameSocket(id);
  const [localGame, setLocalGame] = useState<Game | null>(null);
  const [name, setName] = useState("");
  const [joining, setJoining] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Prefer the most recent snapshot from either the WS stream or a local
  // mutation (e.g. our own move) so the UI never appears stale.
  const game = useMemo(() => {
    if (!localGame) return liveGame;
    if (!liveGame) return localGame;
    return localGame.moveCount >= liveGame.moveCount ? localGame : liveGame;
  }, [liveGame, localGame]);

  const creds = useMemo(() => loadCredentials(id), [id, game]);

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

  async function handleJoin() {
    if (!name.trim()) {
      setError("Choisis un nom");
      return;
    }
    setJoining(true);
    setError(null);
    try {
      const res = await api.joinGame(id, name.trim());
      saveCredentials(id, {
        token: res.token,
        seatIndex: res.seat.index,
        name: name.trim(),
      });
      setLocalGame(res.game);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Erreur inconnue");
    } finally {
      setJoining(false);
    }
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

  return (
    <div className="mx-auto flex h-full max-w-6xl flex-col gap-4 p-4 lg:flex-row">
      <aside className="lg:w-72 flex flex-col gap-4">
        <header className="flex items-baseline justify-between">
          <Link to="/" className="text-lg font-semibold text-zinc-100">
            Gemline
          </Link>
          <div className="flex items-center gap-3">
            <ConnStatus status={wsStatus} attempt={wsAttempt} />
            <UserNav />
          </div>
        </header>

        <Scoreboard game={game} mySeatIndex={creds?.seatIndex ?? null} />

        <Objectives thresholds={game.thresholds} />

        {game.status === "waiting" && !creds && (
          <JoinForm
            value={name}
            onChange={setName}
            onSubmit={handleJoin}
            disabled={joining}
            seatsFree={game.seats.filter((s) => !s.occupied).length}
          />
        )}

        {game.status === "waiting" && creds && (
          <p className="rounded-md border border-zinc-800 bg-zinc-900/50 p-3 text-sm text-zinc-300">
            En attente de{" "}
            {game.seats.filter((s) => !s.occupied).length} joueur(s)…
          </p>
        )}

        {game.status === "finished" && (
          <Banner>
            🏆 {gemName(game.winner)} gagne par {winKindLabel(game.winKind)}
          </Banner>
        )}

        <ShareCard id={id} />

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

      <main className="flex-1 min-h-0">
        <div className="h-full rounded-xl border border-zinc-800 bg-zinc-950/60 p-3">
          <Board
            side={game.boardSide}
            cells={game.cells}
            onPlay={onPlay}
            disabled={!isMyTurn || game.status !== "playing"}
          />
        </div>
      </main>
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

function JoinForm({
  value,
  onChange,
  onSubmit,
  disabled,
  seatsFree,
}: {
  value: string;
  onChange: (v: string) => void;
  onSubmit: () => void;
  disabled: boolean;
  seatsFree: number;
}) {
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit();
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
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
      <button
        type="submit"
        disabled={disabled || seatsFree === 0}
        className="w-full rounded-md bg-yellow-500 px-3 py-2 text-sm font-medium text-zinc-950 transition hover:bg-yellow-400 disabled:opacity-50"
      >
        {disabled ? "..." : "Rejoindre"}
      </button>
    </form>
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
    <div className="rounded-xl border border-yellow-400/40 bg-yellow-400/10 p-3 text-sm text-yellow-200">
      {children}
    </div>
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
    default:
      return "?";
  }
}
