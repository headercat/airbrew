import { type ReactNode, useEffect } from "react";
import { Navigate, useLocation } from "react-router-dom";

import { Skeleton } from "@/components/ui/skeleton";
import { useAuth } from "@/lib/auth";

// Renders children only when the user is signed in; otherwise redirects to
// /login while preserving the originally requested path for post-login return.
export function RequireAuth({ children }: { children: ReactNode }) {
  const { user, loading } = useAuth();
  const location = useLocation();

  if (loading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Skeleton className="h-8 w-48" />
      </div>
    );
  }

  if (!user) {
    return (
      <Navigate
        to="/login"
        replace
        state={{ from: location.pathname + location.search }}
      />
    );
  }

  return <>{children}</>;
}

// Inverse: hides auth pages (login/register) from already-signed-in users.
export function RedirectIfSignedIn({ children }: { children: ReactNode }) {
  const { user, loading } = useAuth();
  useEffect(() => {
    // no-op; just to keep hook ordering stable
  }, [loading]);

  if (loading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Skeleton className="h-8 w-48" />
      </div>
    );
  }

  if (user) return <Navigate to="/" replace />;
  return <>{children}</>;
}
