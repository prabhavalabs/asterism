import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/outfit";
import "@fontsource-variable/oxanium";
import "@fontsource-variable/geist-mono";
import "./styles/tokens.css";
import { initTheme } from "./lib/theme";
import { App } from "./App";

initTheme();

const rootEl = document.getElementById("root");
if (rootEl) {
  createRoot(rootEl).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
}
