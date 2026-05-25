import type { ConnStatus as WsConnStatus } from "../api/gameSocket";

interface ConnStatusProps {
  status: WsConnStatus;
  attempt: number;
}

/**
 * ConnStatus renders the small "en ligne / connexion… / reconnexion / hors-ligne"
 * indicator in the GamePage header. Fed by the per-game WebSocket reconnect
 * state machine (see useGameSocket / gameSocket.ts).
 */
export function ConnStatus({ status, attempt }: ConnStatusProps) {
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
