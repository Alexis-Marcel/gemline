interface SearchingForOpponentProps {
  maxPlayers: number;
  seatsOccupied: number;
  onCancel: () => void;
}

// Waiting room shown when a viewer lands on a public waiting game directly
// (shared URL, or the race between rematch creation and the playing transition).
export function SearchingForOpponent({
  maxPlayers,
  seatsOccupied,
  onCancel,
}: SearchingForOpponentProps) {
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
