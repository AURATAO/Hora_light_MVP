import { Routes, Route, Navigate } from 'react-router-dom'
import Login from './pages/Login.jsx'
import Dashboard from './pages/Dashboard.jsx'
import NewTask from './pages/NewTask.jsx'
import TaskDetail from './pages/TaskDetail.jsx'
import My from './pages/My.jsx'
import PublicProfilePage from './pages/PublicProfilePage.jsx'
import ProtectedLayout from './auth/ProtectLayOut.jsx'
import ShellLayout from './pages/ShellLayout.jsx'


export default function App() {
  return (
    <Routes>
      {/* 公開頁 */}
      <Route path="/login" element={<Login />} />
      <Route path="/u/:id" element={<PublicProfilePage />} />

      {/* 受保護區：外層驗一次，內層帶殼（含 Nav） */}
      <Route element={<ProtectedLayout />}>
        <Route element={<ShellLayout />}>
          <Route index element={<Dashboard />} />
          <Route path="tasks/new" element={<NewTask />} />
          <Route path="tasks/:id" element={<TaskDetail />} />
          <Route path="my" element={<My />} />
        </Route>
      </Route>

      {/* 兜底 */}
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}