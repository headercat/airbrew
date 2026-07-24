import { useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { Coffee, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useAuth } from "@/lib/auth";
import { isApiError } from "@/lib/api";

type LocationState = { from?: string };

export default function LoginPage() {
  const { signIn } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const { t } = useTranslation();
  const from = (location.state as LocationState | null)?.from ?? "/";

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      await signIn(email, password);
      navigate(from, { replace: true });
    } catch (err) {
      setError(
        isApiError(err) ? err.error_description ?? err.error : t("common.error")
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-6">
      <div className="w-full max-w-[360px] space-y-6">
        <div className="space-y-3 text-center">
          <div className="flex justify-center">
            <Coffee className="h-8 w-8 text-primary" />
          </div>
          <div className="space-y-1">
            <h1 className="text-lg font-semibold tracking-tight">
              {t("auth.login.title")}
            </h1>
            <p className="text-sm text-muted-foreground">
              {t("auth.login.subtitle")}
            </p>
          </div>
        </div>

        <Card>
          <CardContent className="p-5">
            <form onSubmit={submit} className="space-y-3.5">
              <div className="space-y-1.5">
                <Label htmlFor="email" className="text-[13px]">
                  {t("auth.login.email")}
                </Label>
                <Input
                  id="email"
                  type="email"
                  autoComplete="email"
                  placeholder="you@example.com"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  required
                  autoFocus
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="password" className="text-[13px]">
                  {t("auth.login.password")}
                </Label>
                <Input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  required
                />
              </div>
              {error && (
                <p className="text-[13px] text-destructive" role="alert">
                  {error}
                </p>
              )}
              <Button type="submit" className="w-full" disabled={busy}>
                {busy && <Loader2 className="h-4 w-4 animate-spin" />}
                {t("auth.login.submit")}
              </Button>
            </form>
          </CardContent>
        </Card>

        <p className="text-center text-[13px] text-muted-foreground">
          {t("auth.login.needAccount")}{" "}
          <Link
            to="/register"
            className="font-medium text-primary hover:underline"
          >
            {t("auth.login.createOne")}
          </Link>
        </p>
      </div>
    </div>
  );
}
