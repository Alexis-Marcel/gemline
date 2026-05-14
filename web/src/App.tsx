import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { AuthProvider } from "./auth/AuthProvider";
import { HomePage } from "./pages/HomePage";
import { GamePage } from "./pages/GamePage";
import { LeaderboardPage } from "./pages/LeaderboardPage";
import { LoginPage, SignupPage } from "./pages/LoginPage";
import { ProfilePage } from "./pages/ProfilePage";
import { PublicProfilePage } from "./pages/PublicProfilePage";

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <div className="min-h-full bg-zinc-950 text-zinc-100">
          <Routes>
            <Route path="/" element={<HomePage />} />
            <Route path="/login" element={<LoginPage />} />
            <Route path="/signup" element={<SignupPage />} />
            <Route path="/profile" element={<ProfilePage />} />
            <Route path="/profile/:userId" element={<PublicProfilePage />} />
            <Route path="/leaderboard" element={<LeaderboardPage />} />
            <Route path="/game/:id" element={<GamePage />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </div>
      </AuthProvider>
    </BrowserRouter>
  );
}
