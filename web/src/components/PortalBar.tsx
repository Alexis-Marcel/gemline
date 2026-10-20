import { useEffect } from "react";
import { useAuth } from "../auth/useAuth";

// The portal Gemline belongs to (PORTAL_URL): a slim bar back to its home, and
// to the player's profile when signed in. Nothing when Gemline lives alone.
// Its height is published as --portal-bar so single-viewport pages (the game,
// matchmaking) subtract it instead of overflowing.
export function PortalBar() {
  const { portal, user } = useAuth();
  const shown = Boolean(portal);
  useEffect(() => {
    if (!shown) return;
    document.documentElement.style.setProperty("--portal-bar", "1.75rem");
    return () => {
      document.documentElement.style.removeProperty("--portal-bar");
    };
  }, [shown]);
  if (!portal) return null;
  return (
    <nav
      aria-label={portal.name}
      className="flex h-7 shrink-0 items-center justify-between gap-4 border-b border-zinc-800/80 bg-black/40 px-4 text-xs text-zinc-400"
    >
      <a href={portal.url} className="inline-flex items-center gap-1.5 hover:text-zinc-200">
        {portal.icon && <img src={portal.icon} alt="" width={16} height={16} className="rounded" />}
        <span>{portal.name}</span>
      </a>
      {user && portal.profileUrl && (
        <a href={portal.profileUrl} className="hover:text-zinc-200">
          Mon profil
        </a>
      )}
    </nav>
  );
}
