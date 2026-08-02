import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { evaluateMasterPassword } from "@/lib/vault/security";

export function PasswordStrengthHint({
  password,
  meter = false,
}: {
  password: string;
  meter?: boolean;
}) {
  const { t } = useTranslation();
  const strength = useMemo(() => evaluateMasterPassword(password), [password]);

  if (!password) {
    return (
      <p className="text-xs text-muted-foreground">
        {t("passwords.strength.hint")}
      </p>
    );
  }

  const text = (
    <p className="text-xs text-muted-foreground">
      {t(strength.labelKey)}
      {strength.feedbackKeys.length > 0
        ? ` · ${strength.feedbackKeys.map((k) => t(k)).join(" · ")}`
        : ""}
    </p>
  );

  if (!meter) return text;

  return (
    <div className="space-y-1">
      <div className="h-1.5 overflow-hidden rounded-full bg-muted">
        <div
          className={
            strength.acceptable ? "h-full bg-green-500" : "h-full bg-amber-500"
          }
          style={{ width: `${Math.max(12, strength.score * 20)}%` }}
        />
      </div>
      {text}
    </div>
  );
}
