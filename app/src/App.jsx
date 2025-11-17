import { Routes, Route, Navigate } from "react-router-dom";
import ShellLayout from "./pages/ShellLayout.jsx";
import ProtectedLayout from "./auth/ProtectLayout.jsx";
import Login from "./pages/Login.jsx";
import Dashboard from "./pages/Dashboard.jsx";
import NewTask from "./pages/NewTask.jsx";
import TaskDetail from "./pages/TaskDetail.jsx";
import My from "./pages/My.jsx";
import PublicProfilePage from "./pages/PublicProfilePage.jsx";
import OpsFeed from "./pages/OpsFeed.jsx";

export default function App() {
  console.log("[App] routes boot");
  return (
    <Routes>
      {/* 公開頁 */}
      <Route path="/login" element={<Login />} />
     

      {/* 受保護區：唯一守門 */}
      <Route element={<ProtectedLayout />}>
        <Route element={<ShellLayout />}>
          <Route path="ops" element={<OpsFeed />} />
          <Route index element={<Dashboard />} />     
          <Route path="tasks/new" element={<NewTask />} />
          <Route path="tasks/:id" element={<TaskDetail />} />
          <Route path="my" element={<My />} />
           <Route path="u/:id" element={<PublicProfilePage />} />
        </Route>
      </Route>

      {/* 兜底：導回首頁（或改成 NotFound 頁） */}
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
