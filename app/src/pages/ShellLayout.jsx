// import { Outlet, useLocation } from 'react-router-dom'
// import Nav from '../components/Nav.jsx'

// export default function ShellLayout() {
//   const loc = useLocation()
//   console.debug('[Shell] mount path=', loc.pathname)
//   return (
//     <>
//       <Nav />
//       <main>
//         <Outlet />
//       </main>
//     </>
//   )
// }

import {  Outlet, useLocation } from "react-router-dom";
import Nav from '../components/Nav.jsx'

export default function ShellLayout() {
  const loc = useLocation();
  console.log("[ShellLayout] render", { path: loc.pathname });

  return (
     <>
       <Nav />
       <main>
         <Outlet />
       </main>
     </>
  );
}