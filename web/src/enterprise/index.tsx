import { type ReactNode } from "react";

// Community build: no commercial license/pretranscode features. The hooks and
// tab lists below keep the same shape as the commercial build so page code
// compiles unchanged, but every commercial feature is reported absent.

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

export function refreshLicenseStatus() {
  // no-op: there is no license to refresh in the community build
}

export function useLicenseStatus(): { status: LicenseStatus | null; loading: boolean } {
  return { status: null, loading: false };
}

export function LicenseGuard({ children }: { children: ReactNode }) {
  return <>{children}</>;
}

// Enterprise tab injection points. The community build leaves these arrays
// empty; the commercial build populates them.
export const enterpriseSystemTabItems: {
  key: string;
  label: string;
  element: ReactNode;
}[] = [];
