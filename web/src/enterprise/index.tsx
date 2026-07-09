import { useEffect, useState, type ReactNode } from "react";
import { Alert, Spin } from "antd";
import { api } from "../api/client";
import { PresetTab } from "../pages/pretranscode/PresetTab";
import { WebhookTab } from "../pages/pretranscode/WebhookTab";

type LicenseStatus = {
  valid?: boolean;
  pretranscode?: boolean;
  trial?: boolean;
  expired?: boolean;
  expiring_soon?: boolean;
  days_remaining?: number;
  trial_limit_met?: boolean;
  error_code?: string;
};

let cached: LicenseStatus | null = null;
let inflight: Promise<LicenseStatus> | null = null;

async function loadStatus(): Promise<LicenseStatus> {
  if (cached) return cached;
  if (inflight) return inflight;
  inflight = api
    .get<LicenseStatus>("/api/v1/admin/license/status")
    .then((r) => r.data)
    .catch(() => ({ valid: false, error_code: "fetch_failed" }))
    .finally(() => {
      inflight = null;
    });
  cached = await inflight;
  return cached;
}

export function refreshLicenseStatus() {
  cached = null;
}

export function useLicenseStatus(): { status: LicenseStatus | null; loading: boolean } {
  const [status, setStatus] = useState<LicenseStatus | null>(null);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let mounted = true;
    loadStatus().then((s) => {
      if (mounted) {
        setStatus(s);
        setLoading(false);
      }
    });
    return () => {
      mounted = false;
    };
  }, []);
  return { status, loading };
}

export function LicenseGuard({ children }: { children: ReactNode }) {
  const { status, loading } = useLicenseStatus();
  if (loading) {
    return <Spin />;
  }
  if (!status || status.expired) {
    return (
      <Alert
        type="error"
        showIcon
        message="License expired"
        description="The commercial license has expired. Pretranscode features are unavailable until a valid license is installed."
      />
    );
  }
  if (!status.pretranscode) {
    return (
      <Alert
        type="warning"
        showIcon
        message="Feature not licensed"
        description="Pretranscode requires a valid commercial license with the pretranscode feature enabled."
      />
    );
  }
  if (status.trial_limit_met) {
    return (
      <Alert
        type="warning"
        showIcon
        message="Trial limit reached"
        description="The trial license allows up to 10 pretranscoded files. Upgrade to a full license to continue."
      />
    );
  }
  return <>{children}</>;
}

// Enterprise tab injection points. The community build leaves these arrays
// empty; the commercial build (this file) populates them.
export const enterpriseSystemTabItems = [
  { key: "pretranscode-presets", label: "pretranscode.preset.tab", element: <LicenseGuard><PresetTab /></LicenseGuard> },
  { key: "pretranscode-webhooks", label: "pretranscode.webhook.tab", element: <LicenseGuard><WebhookTab /></LicenseGuard> },
];
