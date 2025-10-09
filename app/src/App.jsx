import { Routes, Route, Navigate } from 'react-router-dom'
import Login from './pages/Login.jsx'
import Dashboard from './pages/Dashboard.jsx'
import NewTask from './pages/NewTask.jsx'
import TaskDetail from './pages/TaskDetail.jsx'
import My from './pages/My.jsx'
import ProtectedRoute from './auth/ProtectedRoute.jsx'
import Nav from './components/Nav.jsx'
import PublicProfilePage from './pages/PublicProfilePage.jsx'



export default function App() {
return (
    
<div>
    <Nav />
    <main >
    <Routes>
    <Route path="/login" element={<Login />} />


    <Route path="/" element={
        <ProtectedRoute>
        <Dashboard />
        </ProtectedRoute>
        }  />


    <Route
        path="/tasks/new"
    element={
    <ProtectedRoute>
    <NewTask />
    </ProtectedRoute>
        } />


    <Route
        path="/tasks/:id"
        element={
        <ProtectedRoute>
        <TaskDetail />
        </ProtectedRoute>
        }
    />


    <Route
        path="/my"
        element={
        <ProtectedRoute>
        <My />
        </ProtectedRoute>
        }/>


    <Route path="*" element={<Navigate to="/" replace />} />
    <Route path="/u/:id" element={<PublicProfilePage />} />
    </Routes>

    </main>
</div>

)
}