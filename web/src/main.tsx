import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import App from "./App";
import Portal from "./portal/Portal";
import "./style.css";
const admin = window.location.pathname === "/admin" || window.location.pathname.startsWith("/admin/");
ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <BrowserRouter basename={admin ? "/admin" : "/"}>
      {admin ? <App /> : <Portal />}
    </BrowserRouter>
  </React.StrictMode>,
);
