import { Routes, Route, Navigate } from "react-router-dom";
import ShellLayout from "./pages/ShellLayout.jsx";
import ProtectedLayout from "./auth/ProtectLayout.jsx";
import Login from "./pages/Login.jsx";
import CategoryHome from "./pages/CategoryHome.jsx";
import Dashboard from "./pages/Dashboard.jsx";
import NewTask from "./pages/NewTask.jsx";
import TaskDetail from "./pages/TaskDetail.jsx";
import My from "./pages/My.jsx";
import PublicProfilePage from "./pages/PublicProfilePage.jsx";
import OpsFeed from "./pages/OpsFeed.jsx";
import ExternalRedirect from "./components/ExternalRedirect.jsx";
import Profile from "./pages/Profile.jsx";
import ReviewPage from "./pages/ReviewPage.jsx";
import BecomeSupporter from "./pages/BecomeSupporter.jsx";
import OpenInApp from "./pages/OpenInApp.jsx";
import AppleCallback from "./pages/AppleCallback.jsx";

export default function App() {
  console.log("[App] routes boot");
  return (
    <Routes>
      {/* 公開頁 */}
      <Route path="/login" element={<Login />} />
      {/* The legal documents live on my-hora.com — the copies every link in
          both clients points at. These two paths used to render the web
          app's own pages: nothing linked to them, and they had gone stale
          ("Hora Light", March 2026, account deletion "by contacting us"),
          contradicting the in-app deletion both clients ship. Kept as
          redirects so an old link still lands on the current text. */}
      <Route path="/terms" element={<ExternalRedirect to="https://www.my-hora.com/terms" label="Terms of Use" />} />
      <Route path="/privacy" element={<ExternalRedirect to="https://www.my-hora.com/privacy" label="Privacy Policy" />} />
      {/* Where notification-email task links land: bounces into the mobile app
          if it is installed, offers the web task page if it isn't. Public on
          purpose — being signed out is what it exists to solve.
          See app/src/pages/OpenInApp.jsx and server/internal/notify/links.go. */}
      <Route path="/open/task/:id" element={<OpenInApp />} />
      <Route path="/open/task/:id/review" element={<OpenInApp />} />
      {/* Sign in with Apple lands here (Supabase redirectTo) with a PKCE
          code; the page trades it for a session and the hora_session cookie.
          Public: nobody is signed in yet when they arrive. */}
      <Route path="/auth/apple-callback" element={<AppleCallback />} />

      {/* 受保護區：唯一守門 */}
      <Route element={<ProtectedLayout />}>
        <Route element={<ShellLayout />}>
          <Route path="ops" element={<OpsFeed />} />
          <Route index element={<CategoryHome />} />
          <Route path="dashboard" element={<Dashboard />} />
          <Route path="tasks/new" element={<NewTask />} />
          <Route path="tasks/:id" element={<TaskDetail />} />
          <Route path="tasks/:id/review" element={<ReviewPage />} />
          <Route path="my" element={<My />} />
          <Route path="profile" element={<Profile />} />
          {/* Where Stripe sends a supporter back to after payout onboarding —
              it is the return_url and refresh_url the backend mints every
              Account Link with. Renders the profile page, whose Earnings card
              reads ?onboarding= and either re-reads the account or mints a
              fresh link. A dedicated screen would be a second place for the
              same state machine to live. */}
          <Route path="profile/earnings" element={<Profile />} />
          <Route path="become-supporter" element={<BecomeSupporter />} />
          <Route path="u/:id" element={<PublicProfilePage />} />
        </Route>
      </Route>

      {/* 兜底：導回首頁（或改成 NotFound 頁） */}
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
