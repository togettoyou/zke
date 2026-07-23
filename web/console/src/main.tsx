import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App as AntApp, ConfigProvider } from "antd";
import zhCN from "antd/locale/zh_CN";
import { ConsoleApp } from "./ConsoleApp";
import "./styles.css";

const root = document.getElementById("root");

if (!root) {
  throw new Error("Console root element is missing");
}

createRoot(root).render(
  <StrictMode>
    <ConfigProvider
      locale={zhCN}
      theme={{
        token: {
          colorPrimary: "#635bff",
          borderRadius: 10,
          fontFamily:
            '"Inter", "PingFang SC", "Microsoft YaHei", system-ui, sans-serif',
        },
      }}
    >
      <AntApp>
        <ConsoleApp />
      </AntApp>
    </ConfigProvider>
  </StrictMode>,
);
