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
import { HomePage } from "./pages/HomePage";
import { GamePage } from "./pages/GamePage";
import { LeaderboardPage } from "./pages/LeaderboardPage";
import { LoginPage, SignupPage } from "./pages/LoginPage";
import { MatchmakingPage } from "./pages/MatchmakingPage";
import { ProfilePage } from "./pages/ProfilePage";
import { PublicProfilePage } from "./pages/PublicProfilePage";

// Key GamePage by game id so navigating between games (rematch, a new match)
// remounts it — per-game state, sockets, and one-shot effect guards reset
// cleanly instead of leaking across game ids on the shared /game/:id route.
function KeyedGamePage() {
  const { id } = useParams();
  return <GamePage key={id} />;
}

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <InvitationsProvider>
          <div className="min-h-full bg-zinc-950 text-zinc-100">
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
            <InvitationToast />
          </div>
        </InvitationsProvider>
      </AuthProvider>
    </BrowserRouter>
  );
}
