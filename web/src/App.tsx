import { lazy, Suspense } from "react";
import {
  BrowserRouter,
  Navigate,
  Route,
  Routes,
  useParams,
} from "react-router-dom";
import { AuthProvider } from "./auth/AuthProvider";
import { InvitationToast } from "./components/InvitationToast";
import { InvitationsProvider } from "./notifications/InvitationsProvider";

const HomePage = lazy(() =>
  import("./pages/HomePage").then((m) => ({ default: m.HomePage })),
);
const GamePage = lazy(() =>
  import("./pages/GamePage").then((m) => ({ default: m.GamePage })),
);
const LeaderboardPage = lazy(() =>
  import("./pages/LeaderboardPage").then((m) => ({ default: m.LeaderboardPage })),
);
const LoginPage = lazy(() =>
  import("./pages/LoginPage").then((m) => ({ default: m.LoginPage })),
);
const SignupPage = lazy(() =>
  import("./pages/LoginPage").then((m) => ({ default: m.SignupPage })),
);
const MatchmakingPage = lazy(() =>
  import("./pages/MatchmakingPage").then((m) => ({ default: m.MatchmakingPage })),
);
const ProfilePage = lazy(() =>
  import("./pages/ProfilePage").then((m) => ({ default: m.ProfilePage })),
);
const PublicProfilePage = lazy(() =>
  import("./pages/PublicProfilePage").then((m) => ({
    default: m.PublicProfilePage,
  })),
);

// Key by game id so navigating between games remounts GamePage instead of
// leaking per-game state across the shared /game/:id route.
function KeyedGamePage() {
  const { id } = useParams();
  return <GamePage key={id} />;
}

function RouteFallback() {
  return (
    <div className="flex h-screen items-center justify-center bg-zinc-950">
      <div className="h-8 w-8 animate-spin rounded-full border-2 border-zinc-700 border-t-amber-400" />
    </div>
  );
}

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <InvitationsProvider>
          <div className="min-h-full bg-zinc-950 text-zinc-100">
            <Suspense fallback={<RouteFallback />}>
              <Routes>
                <Route path="/" element={<HomePage />} />
                <Route path="/login" element={<LoginPage />} />
                <Route path="/signup" element={<SignupPage />} />
                <Route path="/profile" element={<ProfilePage />} />
                <Route path="/profile/:userId" element={<PublicProfilePage />} />
                <Route path="/leaderboard" element={<LeaderboardPage />} />
                <Route path="/play/1v1" element={<MatchmakingPage mode="1v1" />} />
                <Route path="/play/multi" element={<MatchmakingPage mode="multi" />} />
                <Route path="/game/:id" element={<KeyedGamePage />} />
                <Route path="*" element={<Navigate to="/" replace />} />
              </Routes>
            </Suspense>
            <InvitationToast />
          </div>
        </InvitationsProvider>
      </AuthProvider>
    </BrowserRouter>
  );
}
