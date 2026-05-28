import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import App from "./App";
import AntdLocaleProvider from "./i18n/AntdLocaleProvider";
import { I18nProvider } from "./i18n";
import "./index.css";
import { loadPowerPlayerScript } from "./loadPowerPlayerScript";

loadPowerPlayerScript();

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <I18nProvider>
      <AntdLocaleProvider>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </AntdLocaleProvider>
    </I18nProvider>
  </React.StrictMode>
);
