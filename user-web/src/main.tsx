import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import Portal from "./portal/Portal";
import "./style.css";
ReactDOM.createRoot(document.getElementById("root")!).render(<React.StrictMode><BrowserRouter><Portal /></BrowserRouter></React.StrictMode>);
