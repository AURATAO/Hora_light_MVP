import { Outlet, useLocation } from 'react-router-dom'
import Nav from '../components/Nav.jsx'
import OutstandingBalanceBanner from '../components/OutstandingBalanceBanner.jsx'

export default function ShellLayout() {
  const loc = useLocation()
  console.debug('[Shell] mount path=', loc.pathname)
  return (
    <>
      <Nav />
      {/* Above the page, on every signed-in screen. A balance blocks posting,
          so it has to be visible wherever the requester happens to be rather
          than only on the screen that refuses them. Renders nothing for the
          overwhelming majority who owe nothing. */}
      <div className="mx-auto max-w-3xl px-4 pt-4 empty:hidden">
        <OutstandingBalanceBanner />
      </div>
      <main>
        <Outlet />
      </main>
    </>
  )
}