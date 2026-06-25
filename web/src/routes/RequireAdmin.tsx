import { Navigate, Outlet } from "react-router-dom";
import { isAdminRole, useAuthStore } from "../store/auth";

export default function RequireAdmin() {
  const role = useAuthStore((s) => s.role);
  if (!isAdminRole(role)) {
    return <Navigate to="/" replace />;
  }
  return <Outlet />;
}
