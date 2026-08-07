import { Spin } from "antd";
import type { ReactNode } from "react";
import styles from "./Home.module.css";

export default function HomeLoadingBoundary({ loading, children }: { loading: boolean; children: ReactNode }) {
  if (loading) {
    return (
      <div className={styles.page} style={{ display: "flex", justifyContent: "center", paddingTop: 80 }}>
        <Spin size="large" />
      </div>
    );
  }
  return <>{children}</>;
}
