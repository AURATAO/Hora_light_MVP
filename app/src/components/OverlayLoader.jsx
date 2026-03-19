import React from "react";
import { createPortal } from "react-dom";
import { useLoader } from "../providers/LoaderProvider";
 
export default function OverlayLoader({
  mask = "light", // "none" | "light" | "dark"
  blur = true,
  logoSrc = "/Loading_logo.svg",
}) {
  const { open } = useLoader();
  const maskClass =
    mask === "light"
      ? "bg-black/5"
      : mask === "dark"
      ? "bg-black/20"
      : "bg-transparent";
 
  if (typeof document === "undefined") return null;
 
  return createPortal(
    <div
      aria-busy={open}
      aria-live="polite"
      className={[
        "fixed inset-0 z-9999 grid place-items-center transition-opacity duration-300",
        open
          ? "opacity-100 pointer-events-auto"
          : "opacity-0 pointer-events-none",
        maskClass,
        blur ? "backdrop-blur-sm" : "",
      ].join(" ")}
    >
      <div className="grid place-items-center gap-2 px-4 py-3 rounded-full shadow-lg/10">
        <img
        src={logoSrc}
        alt="loading"
        className="w-8 h-8 object-contain animate-spin motion-reduce:animate-none"
      />
      </div>
    </div>,
    document.body
  );
}
 