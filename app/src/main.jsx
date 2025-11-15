// import React from "react";
// import { createRoot } from "react-dom/client";
// import { BrowserRouter } from "react-router-dom";
// import App from "./App.jsx";
// import { AuthProvider } from "./auth/AuthContext.jsx";
// import { LoaderProvider } from "./providers/LoaderProvider.jsx";
// import OverlayLoader from "./components/OverlayLoader.jsx";
// import "./index.css";
// import loadingIcon from "./assets/Loading_logo.png";

// import { supabase } from "./lib/supabaseClient";
// void supabase.auth.getSession().catch(() => {});



// createRoot(document.getElementById("root")).render(
//   <React.StrictMode>
//     <BrowserRouter>
//       <LoaderProvider minMs={400}>
//         <AuthProvider>
//           <App />
//           {/* 全域透明 Loading，放一次即可 */}
//           <OverlayLoader
//             mask="light"
//             blur
//             logoSrc={loadingIcon}
//             size={64}
//             speedMs={1200}
//           />
//         </AuthProvider>
//       </LoaderProvider>
//     </BrowserRouter>
//   </React.StrictMode>
// );

import React from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import App from "./App.jsx";

import { LoaderProvider } from "./providers/LoaderProvider.jsx";
import { AuthProvider } from "./auth/AuthContext.jsx";
import OverlayLoader from "./components/OverlayLoader.jsx";
import loadingIcon from "./assets/Loading_logo.png";

import "./index.css";

//（可留作除錯）
window.onerror = (msg, src, line, col, err) => {
  console.error("[window.onerror]", msg, { src, line, col, err });
};
window.onunhandledrejection = (e) => {
  console.error("[unhandledrejection]", e?.reason || e);
};

createRoot(document.getElementById("root")).render(
  <BrowserRouter>
    <LoaderProvider minMs={400}>
      <AuthProvider>
        <App />
        <OverlayLoader
            mask="light"
            blur
            logoSrc={loadingIcon}
            size={64}
            speedMs={1200}
          />
      </AuthProvider>
    </LoaderProvider>
  </BrowserRouter>
);
